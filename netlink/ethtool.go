/*
Copyright © 2024 netcfg authors

网卡 offload / Wake-on-LAN：经 ethtool ioctl（github.com/safchain/ethtool，纯 Go，
无需 ethtool 命令）。vishvananda/netlink 不提供 ethtool feature/WoL API，故用此标准库
（非自造轮子）。
*/

package netlink

import (
	"github.com/safchain/ethtool"
)

// EthtoolFeatures 读取网卡当前 offload feature 开关（内核 feature 名 → 是否开启）。
func (m *NetlinkManager) EthtoolFeatures(name string) (map[string]bool, error) {
	e, err := ethtool.NewEthtool()
	if err != nil {
		return nil, err
	}
	defer e.Close()
	return e.Features(name)
}

// EthtoolChange 按内核 feature 名设置 offload 开关。
func (m *NetlinkManager) EthtoolChange(name string, want map[string]bool) error {
	e, err := ethtool.NewEthtool()
	if err != nil {
		return err
	}
	defer e.Close()
	return e.Change(name, want)
}

// SetWakeOnLanMagic 启用 Wake-on-LAN（magic packet，等价 ethtool -s <dev> wol g）。
func (m *NetlinkManager) SetWakeOnLanMagic(name string) error {
	e, err := ethtool.NewEthtool()
	if err != nil {
		return err
	}
	defer e.Close()
	_, err = e.SetWakeOnLan(name, ethtool.WakeOnLan{Opts: ethtool.WAKE_MAGIC})
	return err
}
