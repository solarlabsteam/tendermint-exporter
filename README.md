# tendermint-exporter

![Latest release](https://img.shields.io/github/v/release/solarlabsteam/tendermint-exporter)
[![Actions Status](https://github.com/solarlabsteam/tendermint-exporter/workflows/test/badge.svg)](https://github.com/solarlabsteam/tendermint-exporter/actions)

tendermint-exporter is a Prometheus scraper that scrapes some data to monitor your node, specifically you can set up alerting if:
- your app version does not match the latest on Github (can be useful to be notified on new releases)
- your voting power is 0 for a validator node
- your node is catching up
- your node is not in sync with the reference node (like the foundation one)

## How can I set it up?

First of all, you need to download the latest release from [the releases page](https://github.com/solarlabsteam/tendermint-exporter/releases/). After that, you should unzip it and you are ready to go:

```sh
wget <the link from the releases page>
tar xvfz tendermint-exporter-*
./tendermint-exporter <params>
```

That's not really interesting, what you probably want to do is to have it running in the background. For that, first of all, we have to copy the file to the system apps folder:

```sh
sudo cp ./tendermint-exporter /usr/bin
```

Then we need to create a systemd service for our app:

```sh
sudo nano /etc/systemd/system/tendermint-exporter.service
```

You can use this template (change the user to whatever user you want this to be executed from. It's advised to create a separate user for that instead of running it from root):

```
[Unit]
Description=Cosmos Exporter
After=network-online.target

[Service]
User=<username>
TimeoutStartSec=0
CPUWeight=95
IOWeight=95
ExecStart=tendermint-exporter
Restart=always
RestartSec=2
LimitNOFILE=800000
KillSignal=SIGTERM

[Install]
WantedBy=multi-user.target
```

If you're using cosmovisor, consider adding the same set of env variables as in your cosmovisor's systemd file, otherwise fetching app version would crash.

Then we'll add this service to the autostart and run it:

```sh
sudo systemctl enable tendermint-exporter
sudo systemctl start tendermint-exporter
sudo systemctl status tendermint-exporter # validate it's running
```

If you need to, you can also see the logs of the process:

```sh
sudo journalctl -u tendermint-exporter -f --output cat
```

## How can I scrape data from it?

Here's the example of the Prometheus config you can use for scraping data:

```yaml
scrape-configs:
  - job_name: 'tendermint-exporter'
    scrape_interval: 120s
    static_configs:
      - targets: ['<your IP>:9500']
```

Then restart Prometheus and you're good to go!

## How does it work?

It fetches some data from the local node, another node as a reference node (like `https://rpc.cosmos.network:443` for Cosmos) and GitHub.

## How can I configure it?

You can pass the artuments to the executable file to configure it. Here is the parameters list:

- `--listen-address` - the address with port the node would listen to. For example, you can use it to redefine port or to make the exporter accessible from the outside by listening on `127.0.0.1`. Defaults to `:9500` (so it's accessible from the outside on port 9500)
- `--local-tendermint-rpc` - local Tendermint RPC URL to query node stats. Defaults to `http://localhost:26657`
- `--remote-tendermint-rpc` - remote Tendermint RPC URL to query node stats. Defaults to `http://rpc.cosmos.network:443`
- `--binary-path` - path to a fullnode binary to query version from. It may fail if it's located in $GOPATH and the path is relative, so better to explicitly provide the absolute path.
- `--github-org` - GitHub organization name
- `--github-repo` - Github repository. This param and `--github-org` are used to specify the repository hosting the full node binary sources.
- `--log-devel` - logger level. Defaults to `info`. You can set it to `debug` to make it more verbose.

Additionally, you can pass a `--config` flag with a path to your config file (I use `.toml`, but anything supported by [viper](https://github.com/spf13/viper) should work).

## How can I contribute?

Bug reports and feature requests are always welcome! If you want to contribute, feel free to open issues or PRs.
