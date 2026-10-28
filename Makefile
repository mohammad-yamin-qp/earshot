PREFIX ?= $(HOME)/.local
PLUGIN_DIR ?= $(HOME)/.local/share/noctalia/plugins/earshot

build:
	go build -trimpath -ldflags="-s -w" -o earshot .

install: build
	install -Dm755 earshot $(PREFIX)/bin/earshot
	install -Dm755 contrib/earshot-toggle $(PREFIX)/bin/earshot-toggle
	install -Dm644 contrib/earshot.service $(HOME)/.config/systemd/user/earshot.service
	@test -f $(HOME)/.config/earshot/env || install -Dm600 contrib/env.example $(HOME)/.config/earshot/env
	systemctl --user daemon-reload
	systemctl --user enable --now earshot
	systemctl --user restart earshot

# Symlink the Noctalia bar widget so edits in contrib/noctalia hot-reload.
plugin:
	@if [ -d $(PLUGIN_DIR) ] && [ ! -L $(PLUGIN_DIR) ]; then mv $(PLUGIN_DIR) $(HOME)/.local/share/noctalia/earshot-plugin.bak; echo "moved old plugin dir to ~/.local/share/noctalia/earshot-plugin.bak"; fi
	mkdir -p $(dir $(PLUGIN_DIR))
	ln -sfn $(CURDIR)/contrib/noctalia $(PLUGIN_DIR)
	noctalia msg plugins enable yamin/earshot

.PHONY: build install plugin
