package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
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
	BinaryPath          string
	LogLevel            string

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

type Data struct {
	releaseInfo  ReleaseInfo
	versionInfo  VersionInfo
	localStatus  *coretypes.ResultStatus
	remoteStatus *coretypes.ResultStatus
	err          error
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

	data := GetAllData()
	if data.err != nil {
		log.Error().Err(data.err).Msg("Could not fetch some data")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Error fetching data: " + data.err.Error()))
		return
	}

	nodeCatchingUpGauge.With(prometheus.Labels{
		"id":      string(data.localStatus.NodeInfo.DefaultNodeID),
		"moniker": data.localStatus.NodeInfo.Moniker,
	}).Set(BoolToFloat64(data.localStatus.SyncInfo.CatchingUp))

	votingPower.With(prometheus.Labels{
		"id":      string(data.localStatus.NodeInfo.DefaultNodeID),
		"moniker": data.localStatus.NodeInfo.Moniker,
	}).Set(float64(data.localStatus.ValidatorInfo.VotingPower))

	appVersion.With(prometheus.Labels{
		"id":      string(data.localStatus.NodeInfo.DefaultNodeID),
		"moniker": data.localStatus.NodeInfo.Moniker,
		"version": data.versionInfo.Version,
	}).Set(1)

	githubLatestVersion.With(prometheus.Labels{
		"organization": GithubOrg,
		"repository":   GithubRepo,
		"version":      data.releaseInfo.TagName,
	}).Set(1)

	versionMismatch := !(strings.Contains(data.releaseInfo.TagName, data.versionInfo.Version) || strings.Contains(data.versionInfo.Version, data.releaseInfo.TagName))

	latestVersionMismatch.With(prometheus.Labels{
		"id":             string(data.localStatus.NodeInfo.DefaultNodeID),
		"moniker":        data.localStatus.NodeInfo.Moniker,
		"local_version":  data.versionInfo.Version,
		"remote_version": data.releaseInfo.TagName,
	}).Set(BoolToFloat64(versionMismatch))

	localNodeLatestBlock.With(prometheus.Labels{
		"id":      string(data.localStatus.NodeInfo.DefaultNodeID),
		"moniker": data.localStatus.NodeInfo.Moniker,
	}).Set(float64(data.localStatus.SyncInfo.LatestBlockHeight))

	remoteNodeLatestBlock.With(prometheus.Labels{
		"id":      string(data.remoteStatus.NodeInfo.DefaultNodeID),
		"moniker": data.remoteStatus.NodeInfo.Moniker,
	}).Set(float64(data.remoteStatus.SyncInfo.LatestBlockHeight))

	h := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	h.ServeHTTP(w, r)
}

func BoolToFloat64(value bool) float64 {
	if value {
		return 1
	}

	return 0
}

func GetAllData() Data {
	var (
		wg                sync.WaitGroup
		localStatus       *coretypes.ResultStatus
		remoteStatus      *coretypes.ResultStatus
		releaseInfo       ReleaseInfo
		versionInfo       VersionInfo
		localStatusError  error
		remoteStatusError error
		releaseInfoError  error
		versionInfoError  error
	)

	go func() {
		localStatus, localStatusError = GetNodeStatus(LocalTendermintRpc)
		wg.Done()
	}()
	wg.Add(1)

	go func() {
		remoteStatus, remoteStatusError = GetNodeStatus(RemoteTendermintRpc)
		wg.Done()
	}()
	wg.Add(1)

	go func() {
		latestReleaseUrl := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", GithubOrg, GithubRepo)
		releaseInfoError = GetJson(latestReleaseUrl, &releaseInfo)
		wg.Done()
	}()
	wg.Add(1)

	go func() {
		versionInfo, versionInfoError = GetNodeVersion()
		wg.Done()
	}()
	wg.Add(1)

	wg.Wait()

	if localStatusError != nil {
		log.Error().Err(localStatusError).Msg("Could not query local Tendermint status")
		return Data{err: localStatusError}
	}

	if localStatusError != nil {
		log.Error().Err(localStatusError).Msg("Could not query remote Tendermint status")
		return Data{err: remoteStatusError}
	}

	if releaseInfoError != nil {
		log.Error().Err(releaseInfoError).Msg("Could not fetch latest version")
		return Data{err: releaseInfoError}
	}

	if versionInfoError != nil {
		log.Error().Err(versionInfoError).Msg("Could not fetch app version")
		return Data{err: versionInfoError}
	}

	return Data{
		releaseInfo:  releaseInfo,
		versionInfo:  versionInfo,
		localStatus:  localStatus,
		remoteStatus: remoteStatus,
	}
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
	out, err := exec.Command(BinaryPath, "version", "--long", "--output", "json").CombinedOutput()
	if err != nil {
		log.Error().Err(err).Str("output", string(out)).Msg("Could not get app version")
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
	rootCmd.PersistentFlags().StringVar(&RemoteTendermintRpc, "remote-tendermint-rpc", "https://rpc.cosmos.network:443", "Remote Tendermint RPC address")
	rootCmd.PersistentFlags().StringVar(&LocalTendermintRpc, "local-tendermint-rpc", "http://localhost:26657", "Local Tendermint RPC address")
	rootCmd.PersistentFlags().StringVar(&BinaryPath, "binary-path", "gaia", "Binary path to get version from")
	rootCmd.PersistentFlags().StringVar(&GithubOrg, "github-org", "cosmos", "Github organization name")
	rootCmd.PersistentFlags().StringVar(&GithubRepo, "github-repo", "gaia", "Github repository name")

	if err := rootCmd.Execute(); err != nil {
		log.Fatal().Err(err).Msg("Could not start application")
	}
}
