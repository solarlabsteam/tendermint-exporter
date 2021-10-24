package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	tmrpc "github.com/tendermint/tendermint/rpc/client/http"
	coretypes "github.com/tendermint/tendermint/rpc/core/types"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/rs/zerolog"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

var (
	ConfigPath string

	ListenAddress       string
	LocalTendermintRpc  string
	RemoteTendermintRpc string
	BinaryName          string
	LogLevel            string
	Limit               uint64

	GithubOrg  string
	GithubRepo string
)

type VersionInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type ReleaseInfo struct {
	Name    string `json:"name"`
	TagName string `json:"tag_name"`
}

var log = zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout}).With().Timestamp().Logger()

var myClient = &http.Client{Timeout: 10 * time.Second}

var rootCmd = &cobra.Command{
	Use:  "tendermint-exporter",
	Long: "Scrape the data on Tendermint node.",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if ConfigPath == "" {
			log.Info().Msg("Config file not provided")
			return nil
		}

		log.Info().Msg("Config file provided")

		viper.SetConfigFile(ConfigPath)
		if err := viper.ReadInConfig(); err != nil {
			log.Info().Err(err).Msg("Error reading config file")
			if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
				return err
			}
		}

		// Credits to https://carolynvanslyck.com/blog/2020/08/sting-of-the-viper/
		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			if !f.Changed && viper.IsSet(f.Name) {
				val := viper.Get(f.Name)
				if err := cmd.Flags().Set(f.Name, fmt.Sprintf("%v", val)); err != nil {
					log.Fatal().Err(err).Msg("Could not set flag")
				}
			}
		})

		return nil
	},
	Run: Execute,
}

func Execute(cmd *cobra.Command, args []string) {
	logLevel, err := zerolog.ParseLevel(LogLevel)
	if err != nil {
		log.Fatal().Err(err).Msg("Could not parse log level")
	}

	zerolog.SetGlobalLevel(logLevel)

	log.Info().
		Str("--listen-address", ListenAddress).
		Str("--tendermint-rpc", LocalTendermintRpc).
		Str("--log-level", LogLevel).
		Msg("Started with following parameters")

	http.HandleFunc("/metrics", Handler)

	log.Info().Str("address", ListenAddress).Msg("Listening")
	err = http.ListenAndServe(ListenAddress, nil)
	if err != nil {
		log.Fatal().Err(err).Msg("Could not start application")
	}
}

func Handler(w http.ResponseWriter, r *http.Request) {
	nodeCatchingUpGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "tendermint_node_catching_up",
			Help: "Is node catching up?",
		},
		[]string{"id", "moniker"},
	)

	appVersion := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "tendermint_node_app_version",
			Help: "App version",
		},
		[]string{"id", "moniker", "version"},
	)

	votingPower := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "tendermint_node_voting_power",
			Help: "Voting power",
		},
		[]string{"id", "moniker"},
	)

	githubLatestVersion := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "tendermint_github_latest_version",
			Help: "Github latest version",
		},
		[]string{"organization", "repository", "version"},
	)

	latestVersionMismatch := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "tendermint_latest_version_mismatch",
			Help: "If using the latest version or not",
		},
		[]string{"id", "moniker", "local_version", "remote_version"},
	)

	localNodeLatestBlock := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "tendermint_local_node_latest_block",
			Help: "Local node latest block",
		},
		[]string{"id", "moniker"},
	)

	remoteNodeLatestBlock := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "tendermint_remote_node_latest_block",
			Help: "Local node latest block?",
		},
		[]string{"id", "moniker"},
	)

	registry := prometheus.NewRegistry()
	registry.MustRegister(nodeCatchingUpGauge)
	registry.MustRegister(appVersion)
	registry.MustRegister(votingPower)
	registry.MustRegister(githubLatestVersion)
	registry.MustRegister(latestVersionMismatch)
	registry.MustRegister(localNodeLatestBlock)
	registry.MustRegister(remoteNodeLatestBlock)

	localStatus, err := GetNodeStatus(LocalTendermintRpc)
	if err != nil {
		log.Error().Err(err).Msg("Could not query local Tendermint status")
		return
	}

	remoteStatus, err := GetNodeStatus(RemoteTendermintRpc)
	if err != nil {
		log.Error().Err(err).Msg("Could not query remote Tendermint status")
		return
	}

	latestReleaseUrl := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", GithubOrg, GithubRepo)
	var releaseInfo ReleaseInfo

	err = GetJson(latestReleaseUrl, &releaseInfo)
	if err != nil {
		log.Error().Err(err).Msg("Could not fetch latest version")
		return
	}

	versionInfo, err := GetNodeVersion()
	if err != nil {
		log.Error().Err(err).Msg("Could not unmarshall app version")
		return
	}

	nodeCatchingUpGauge.With(prometheus.Labels{
		"id":      string(localStatus.NodeInfo.DefaultNodeID),
		"moniker": localStatus.NodeInfo.Moniker,
	}).Set(BoolToFloat64(localStatus.SyncInfo.CatchingUp))

	votingPower.With(prometheus.Labels{
		"id":      string(localStatus.NodeInfo.DefaultNodeID),
		"moniker": localStatus.NodeInfo.Moniker,
	}).Set(float64(localStatus.ValidatorInfo.VotingPower))

	appVersion.With(prometheus.Labels{
		"id":      string(localStatus.NodeInfo.DefaultNodeID),
		"moniker": localStatus.NodeInfo.Moniker,
		"version": versionInfo.Version,
	}).Set(1)

	githubLatestVersion.With(prometheus.Labels{
		"organization": GithubOrg,
		"repository":   GithubRepo,
		"version":      releaseInfo.TagName,
	}).Set(1)

	versionMismatch := !(strings.Contains(releaseInfo.TagName, versionInfo.Version) || strings.Contains(versionInfo.Version, releaseInfo.TagName))

	latestVersionMismatch.With(prometheus.Labels{
		"id":             string(localStatus.NodeInfo.DefaultNodeID),
		"moniker":        localStatus.NodeInfo.Moniker,
		"local_version":  versionInfo.Version,
		"remote_version": releaseInfo.TagName,
	}).Set(BoolToFloat64(versionMismatch))

	localNodeLatestBlock.With(prometheus.Labels{
		"id":      string(localStatus.NodeInfo.DefaultNodeID),
		"moniker": localStatus.NodeInfo.Moniker,
	}).Set(float64(localStatus.SyncInfo.LatestBlockHeight))

	remoteNodeLatestBlock.With(prometheus.Labels{
		"id":      string(remoteStatus.NodeInfo.DefaultNodeID),
		"moniker": remoteStatus.NodeInfo.Moniker,
	}).Set(float64(remoteStatus.SyncInfo.LatestBlockHeight))

	h := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	h.ServeHTTP(w, r)
}

func BoolToFloat64(value bool) float64 {
	if value {
		return 1
	}

	return 0
}

func GetJson(url string, target interface{}) error {
	r, err := myClient.Get(url)
	if err != nil {
		return err
	}
	defer r.Body.Close()

	return json.NewDecoder(r.Body).Decode(target)
}

func GetNodeStatus(nodeUrl string) (*coretypes.ResultStatus, error) {
	client, err := tmrpc.New(nodeUrl, "/websocket")
	if err != nil {
		return nil, err
	}

	return client.Status(context.Background())
}

func GetNodeVersion() (VersionInfo, error) {
	out, err := exec.Command(BinaryName, "version", "--long", "--output", "json").Output()
	if err != nil {
		return VersionInfo{}, err
	}

	var versionInfo VersionInfo
	if err := json.Unmarshal(out, &versionInfo); err != nil {
		log.Error().Err(err).Msg("Could not unmarshall app version")
		return versionInfo, err
	}

	return versionInfo, nil
}

func main() {
	rootCmd.PersistentFlags().StringVar(&ConfigPath, "config", "", "Config file path")
	rootCmd.PersistentFlags().StringVar(&ListenAddress, "listen-address", ":9500", "The address this exporter would listen on")
	rootCmd.PersistentFlags().StringVar(&LogLevel, "log-level", "info", "Logging level")
	rootCmd.PersistentFlags().StringVar(&RemoteTendermintRpc, "remote-tendermint-rpc", "https://rpc.sentinel.co:443", "Remote Tendermint RPC address")
	rootCmd.PersistentFlags().StringVar(&LocalTendermintRpc, "local-tendermint-rpc", "http://localhost:26657", "Local Tendermint RPC address")
	rootCmd.PersistentFlags().StringVar(&BinaryName, "binary-name", "sentinelhub", "Binary version to get version from")
	rootCmd.PersistentFlags().StringVar(&GithubOrg, "github-org", "sentinel-official", "Github organization name")
	rootCmd.PersistentFlags().StringVar(&GithubRepo, "github-repo", "hub", "Github repository name")

	if err := rootCmd.Execute(); err != nil {
		log.Fatal().Err(err).Msg("Could not start application")
	}
}
