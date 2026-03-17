BINARY    = vk-mattermost
CMD       = .
INSTALL   = $(HOME)/.local/bin/$(BINARY)

.PHONY: build install uninstall restart clean

build:
	go build -mod=mod -o $(BINARY) $(CMD)

install: build
	mkdir -p $(HOME)/.local/bin
	systemctl --user stop $(BINARY) || true
	cp $(BINARY) $(INSTALL)
	systemctl --user daemon-reload
	systemctl --user start $(BINARY)
	@echo "Installed to $(INSTALL)"

uninstall:
	systemctl --user stop $(BINARY) || true
	systemctl --user disable $(BINARY) || true
	rm -f $(INSTALL)

restart:
	systemctl --user restart $(BINARY)

clean:
	rm -f $(BINARY)
