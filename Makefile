.PHONY: build install clean test fmt lint enable disable start stop status

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.2.0")
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
SYSTEMD_DIR := /etc/systemd/system

LDFLAGS := -X github.com/netcfg/netcfg/version.Version=$(VERSION) \
           -X github.com/netcfg/netcfg/version.GitCommit=$(GIT_COMMIT) \
           -X github.com/netcfg/netcfg/version.BuildDate=$(BUILD_DATE)

build:
	go build -ldflags "$(LDFLAGS)" -o netcfg .

install: build
	install -Dm755 netcfg /usr/bin/netcfg
	# Create symlink for netplan compatibility
	ln -sf /usr/bin/netcfg /usr/bin/netplan
	# Create config directory
	mkdir -p /etc/netcfg
	mkdir -p /etc/netplan
	# Install systemd services
	install -Dm644 systemd/netcfg-apply.service $(SYSTEMD_DIR)/netcfg-apply.service
	install -Dm644 systemd/netcfg.service $(SYSTEMD_DIR)/netcfg.service
	install -Dm644 systemd/netcfg-netns.service $(SYSTEMD_DIR)/netcfg-netns.service
	install -Dm644 systemd/netcfg-wait-online.service $(SYSTEMD_DIR)/netcfg-wait-online.service
	systemctl daemon-reload
	@echo ""
	@echo "安装完成! 启用开机自启动:"
	@echo "  sudo make enable"

uninstall: disable
	rm -f /usr/bin/netcfg
	rm -f /usr/bin/netplan
	rm -f $(SYSTEMD_DIR)/netcfg*.service
	systemctl daemon-reload

# Systemd 管理命令
enable:
	systemctl enable netcfg-apply.service
	@echo "netcfg-apply 已启用（开机自动应用网络配置）"

disable:
	-systemctl disable netcfg-apply.service 2>/dev/null
	-systemctl disable netcfg.service 2>/dev/null
	-systemctl disable netcfg-netns.service 2>/dev/null
	-systemctl disable netcfg-wait-online.service 2>/dev/null

start:
	systemctl start netcfg.service

stop:
	systemctl stop netcfg.service

restart:
	systemctl restart netcfg.service

status:
	systemctl status netcfg.service

clean:
	rm -f netcfg
	rm -rf dist/

test:
	go test -v ./...

fmt:
	go fmt ./...

lint:
	golangci-lint run

# Build for multiple architectures
dist:
	mkdir -p dist
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/netcfg_linux_amd64 .
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/netcfg_linux_arm64 .
	GOOS=linux GOARCH=arm GOARM=7 go build -ldflags "$(LDFLAGS)" -o dist/netcfg_linux_armv7 .

# Build deb package
deb: build
	mkdir -p dist/deb/DEBIAN
	mkdir -p dist/deb/usr/bin
	mkdir -p dist/deb/etc/netcfg
	mkdir -p dist/deb/usr/share/doc/netcfg
	cp netcfg dist/deb/usr/bin/
	cp debian/control dist/deb/DEBIAN/
	cp debian/postinst dist/deb/DEBIAN/
	chmod 755 dist/deb/DEBIAN/postinst
	cp README.md dist/deb/usr/share/doc/netcfg/
	cp example/*.yaml dist/deb/usr/share/doc/netcfg/
	dpkg-deb --build dist/deb dist/netcfg_$(VERSION)_amd64.deb

# Build rpm package  
rpm: build
	mkdir -p ~/rpmbuild/{SPECS,SOURCES,BUILD,RPMS,SRPMS}
	cp netcfg ~/rpmbuild/SOURCES/
	cp rpm/netcfg.spec ~/rpmbuild/SPECS/
	rpmbuild -bb ~/rpmbuild/SPECS/netcfg.spec
