# earshot

Record the room's microphone while you're away, and listen from your phone live or later.

A small Go daemon captures the default PipeWire/PulseAudio source with ffmpeg and:

- **archives** it as hourly, clock-aligned Opus files (~14 MB/hour at 32 kbps)
- **streams** it live as MP3 to any number of browsers (~0.1 s to first audio)
- serves a **mobile web UI** to start/stop, listen live, and play, download, or delete recordings

It's controlled over a tiny HTTP API, so it can be driven from the phone, a keybind, or the
included [Noctalia](https://github.com/noctalia-dev) bar widget.

```
 bar widget / keybind / phone ──► HTTP API ──► earshot ──► ffmpeg ◄── mic (PipeWire)
                                                 │
                         ~/Recordings/earshot/*.opus   /live.mp3 ──► phone browser
```

## Requirements

- Linux with PipeWire (`pipewire-pulse`) or PulseAudio, plus `wpctl` (WirePlumber)
- `ffmpeg` built with `libopus` and `libmp3lame`
- Go 1.22+ to build
- systemd (user services + `systemd-inhibit`)
- Optional: `notify-send` for the toggle script, Noctalia v5 for the bar widget

## Install

```sh
make install   # build, install to ~/.local/bin, enable + start the systemd user service
make plugin    # optional: symlink and enable the Noctalia bar widget
```

`make install` copies `contrib/env.example` to `~/.config/earshot/env` the first time. Edit that
file, then run `systemctl --user restart earshot` to apply changes.

### Phone access (LAN)

The daemon listens on `:8787` with no password, so anyone on your network can reach it. Allow
the port from your LAN only, for example with ufw:

```sh
sudo ufw allow from 192.168.1.0/24 to any port 8787 proto tcp
```

Then open `http://<laptop-ip>:8787` (or `http://<hostname>.local:8787` with Avahi) on the phone
and use **Add to Home Screen**. To require a password, set `EARSHOT_PASSWORD`. Clients then
need basic auth with any username, except localhost, which is always allowed.

### Access from anywhere (Tailscale)

Install [Tailscale](https://tailscale.com) on the laptop and the phone and sign in to the same
tailnet. Allow the port on the Tailscale interface:

```sh
sudo ufw allow in on tailscale0 to any port 8787 proto tcp
```

Then open `http://<machine-name>:8787` (MagicDNS) or `http://100.x.y.z:8787` from the phone,
on any network. Live audio uses about 30 MB/hour of mobile data.

For HTTPS with a real certificate, which makes the home-screen app behave better on iOS:

```sh
sudo tailscale serve --bg 8787   # → https://<machine>.<tailnet>.ts.net
```

Requests through `serve` arrive from localhost, which `EARSHOT_PASSWORD` doesn't check, so use
the plain `:8787` URL if you rely on the password. Never use `tailscale funnel`, which would
make the mic public.

### Start at boot

User services start at login. To have earshot running after an unattended reboot, enable
lingering:

```sh
sudo loginctl enable-linger $USER
```

Set `EARSHOT_AUTOSTART=1` to start recording as soon as the daemon starts.

## Controls

| Where | How |
|---|---|
| Phone / browser | `http://<host>:8787`: Start/Stop, ▶ Listen, recordings list |
| Keybind | `earshot-toggle` toggles and sends a desktop notification. In niri: `Mod+Shift+R { spawn "earshot-toggle"; }` |
| Noctalia bar | left click toggles, right click opens the web UI; red mic + timer while recording |
| Shell | `curl -X POST localhost:8787/api/toggle` |

### Noctalia widget

`make plugin` symlinks `contrib/noctalia` into `~/.local/share/noctalia/plugins/earshot`, so
edits to `widget.luau` hot-reload. Define the widget in `~/.config/noctalia/config.toml`:

```toml
[widget.earshot]
type = "yamin/earshot:toggle"
```

Then add `"earshot"` to a bar list such as `[bar.main] end = [...]`. If you've saved bar
layout from Noctalia's settings window, that list lives in `~/.local/state/noctalia/settings.toml`
and **overrides** the one in `config.toml`, so add it there (or drag it in via Settings → Bar).

## Configuration

Environment variables, set in `~/.config/earshot/env`:

| Variable | Default | Meaning |
|---|---|---|
| `EARSHOT_ADDR` | `:8787` | Listen address |
| `EARSHOT_DIR` | `~/Recordings/earshot` | Where Opus files are written |
| `EARSHOT_KEEP_DAYS` | `14` | Delete recordings older than this (`0` keeps forever) |
| `EARSHOT_BITRATE` | `32k` | Opus archive bitrate |
| `EARSHOT_AUTOSTART` | `0` | `1` starts recording when the daemon starts |
| `EARSHOT_PASSWORD` | *(empty)* | Require basic auth for non-localhost clients |
| `EARSHOT_INHIBIT` | `sleep:idle:handle-lid-switch` | What `systemd-inhibit` blocks while recording |

## HTTP API

| Method & path | Description |
|---|---|
| `GET /api/status` | `{recording, since, elapsed, listeners}` |
| `POST /api/start` · `/api/stop` · `/api/toggle` | Control recording; returns status |
| `GET /api/recordings` | List of `{name, size, modified, live}`, newest first |
| `DELETE /api/recordings/{name}` | Delete a recording |
| `GET /rec/{name}` | Download/stream an Opus file (supports Range/seeking) |
| `GET /live.mp3` | Live MP3 stream (409 when not recording) |

## Behaviour notes

- **Mic mute:** if the default source is muted, earshot unmutes it while recording and mutes it
  again afterwards. A `SIGKILL`ed daemon can't restore it.
- **Sleep:** while recording, a `systemd-inhibit` lock blocks suspend, idle, and lid-close. The
  lock is tied to a pipe from the daemon, so it's released even if the daemon crashes.
- **Clean stop:** ffmpeg is stopped by writing `q` to its stdin, which finalizes the Opus file
  even if you stop it during startup.
- **Live stream:** MP3 is used because Ogg/Opus playback is unreliable on iOS Safari. Listeners
  that can't keep up are disconnected rather than slowing down the recording.
- Recording does **not** resume after a reboot unless `EARSHOT_AUTOSTART=1`.

## Logs

```sh
journalctl --user -u earshot -f
```

## Legal

Recording your own space is generally fine. In many places, recording other people's
conversations without their consent is illegal. Make sure anyone who'll be in the room knows
about it.
