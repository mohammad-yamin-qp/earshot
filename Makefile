PREFIX ?= $(HOME)/.local

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

.PHONY: build install
