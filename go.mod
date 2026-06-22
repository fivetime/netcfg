module github.com/netcfg/netcfg

go 1.25.0

// VPP 后端用 GoVPP（go.fd.io/govpp，要求 go 1.25）。开发期指向本地 govpp 源，
// 版本精确、离线；正式发布前可改为带版本的 require。
replace go.fd.io/govpp => C:/MyProjects/OpenSource/Kubernetes/govpp

// netlink 指向本地上游最新检出（含 seg6 VrfTable/全 action、幂等 RouteReplace）；
// require 行保留 v1.1.0，由 replace 接管源码。正式发布前可改回带版本 require。
replace github.com/vishvananda/netlink => C:/MyProjects/OpenSource/Kubernetes/netlink

require (
	github.com/insomniacslk/dhcp v0.0.0-20260603135910-a415979eb11e
	github.com/spf13/cobra v1.10.2
	github.com/vishvananda/netlink v1.1.0
	github.com/vishvananda/netns v0.0.5
	go.fd.io/govpp v0.0.0-00010101000000-000000000000
	golang.org/x/sys v0.40.0
	golang.zx2c4.com/wireguard/wgctrl v0.0.0-20241231184526-a9ab2273dd10
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/josharian/native v1.1.0 // indirect
	github.com/lunixbochs/struc v0.0.0-20200521075829-a4cb8d33dbbe // indirect
	github.com/mdlayher/genetlink v1.3.2 // indirect
	github.com/mdlayher/netlink v1.7.2 // indirect
	github.com/mdlayher/packet v1.1.2 // indirect
	github.com/mdlayher/socket v0.5.1 // indirect
	github.com/pierrec/lz4/v4 v4.1.14 // indirect
	github.com/sirupsen/logrus v1.9.4 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/u-root/uio v0.0.0-20230220225925-ffce2a382923 // indirect
	golang.org/x/crypto v0.47.0 // indirect
	golang.org/x/net v0.49.0 // indirect
	golang.org/x/sync v0.10.0 // indirect
	golang.zx2c4.com/wireguard v0.0.0-20231211153847-12269c276173 // indirect
)
