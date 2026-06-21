# cloud-init Integration

netcfg 可作为 cloud-init 的网络渲染器，替代 netplan/eni/sysconfig。

## 架构

```
cloud-init
    │
    ├── 数据源 (DataSource)
    │   ├── NoCloud
    │   ├── ConfigDrive (OpenStack)
    │   ├── EC2 (AWS)
    │   ├── GCE (Google)
    │   └── Azure
    │
    └── 网络渲染器 (Renderer)
        └── netcfg ← 本插件
            ├── 生成 /etc/netplan/50-cloud-init.yaml
            └── 调用 netcfg apply
```

## 运行模型（一次性，无需常驻）

cloud-init 渲染器对 netcfg 的调用是**一次性**的：cloud-init（开机运行、跑完即退）生成 `/etc/netplan/50-cloud-init.yaml` 后调用一次 `netcfg apply`，netcfg 把配置写入内核即退出。**cloud-init 集成不需要 netcfg 常驻进程。**

唯一需要常驻的是 **DHCP 租约续期**（协议要求）——云上 VM 多用 DHCP，可由可选的 `netcfg daemon`（或外部 dhclient/dhcpcd 自续约）负责；这与配置来自 cloud-init 还是手动 apply 无关。

netcfg **不依赖 systemd-networkd / D-Bus**，可在任意 init 系统（systemd / OpenRC / runit 等）的 cloud-init 环境中工作。

## 安装

```bash
# 1. 安装 netcfg
sudo make install

# 2. 安装 cloud-init 渲染器
sudo ./cloud-init/install.sh
```

## 手动配置

如果自动安装失败，手动配置：

### 1. 复制渲染器

```bash
# 找到 cloud-init 渲染器目录
RENDERER_DIR=$(python3 -c "import cloudinit.net.renderers; print(cloudinit.net.renderers.__path__[0])")

# 复制
sudo cp cloud-init/netcfg.py $RENDERER_DIR/
```

### 2. 注册渲染器

编辑 `$RENDERER_DIR/__init__.py`，在 `NAME_TO_RENDERER` 字典中添加：

```python
NAME_TO_RENDERER = {
    "netcfg": "cloudinit.net.renderers.netcfg",  # 添加这行
    "netplan": "cloudinit.net.renderers.netplan",
    # ...
}
```

### 3. 配置 cloud-init 优先使用 netcfg

```bash
cat > /etc/cloud/cloud.cfg.d/99-netcfg.cfg << 'EOF'
network:
  renderers: ['netcfg', 'netplan', 'eni', 'sysconfig']
EOF
```

## 测试

```bash
# 清除 cloud-init 状态
sudo cloud-init clean --logs

# 重新初始化
sudo cloud-init init --local

# 检查生成的配置
cat /etc/netplan/50-cloud-init.yaml

# 检查网络状态
netcfg status
```

## 支持的配置格式

### cloud-init Version 1 (自动转换)

```yaml
network:
  version: 1
  config:
    - type: physical
      name: eth0
      mac_address: "00:11:22:33:44:55"
      subnets:
        - type: static
          address: 192.168.1.10
          netmask: 255.255.255.0
          gateway: 192.168.1.1
        - type: dhcp
    - type: bond
      name: bond0
      bond_interfaces:
        - eth1
        - eth2
      params:
        bond-mode: 802.3ad
      subnets:
        - type: dhcp
    - type: vlan
      name: vlan100
      vlan_link: eth0
      vlan_id: 100
      subnets:
        - type: static
          address: 10.0.100.10/24
```

### cloud-init Version 2 / netplan (原生支持)

```yaml
network:
  version: 2
  ethernets:
    eth0:
      dhcp4: true
  bonds:
    bond0:
      interfaces: [eth1, eth2]
      parameters:
        mode: 802.3ad
      dhcp4: true
```

## 数据源示例

### NoCloud (本地 ISO)

```bash
# 创建 meta-data
cat > meta-data << 'EOF'
instance-id: test-vm-001
local-hostname: testvm
EOF

# 创建 network-config
cat > network-config << 'EOF'
version: 2
ethernets:
  eth0:
    dhcp4: true
EOF

# 创建 ISO
genisoimage -output seed.iso -volid cidata -joliet -rock meta-data network-config
```

### ConfigDrive (OpenStack)

ConfigDrive 会自动挂载到 `/mnt/config`，cloud-init 从中读取：
- `/mnt/config/openstack/latest/network_data.json`
- `/mnt/config/openstack/latest/meta_data.json`

### HTTP Metadata (AWS/GCE/Azure)

cloud-init 自动从元数据服务获取：
- AWS: `http://169.254.169.254/latest/meta-data/`
- GCE: `http://metadata.google.internal/computeMetadata/v1/`
- Azure: `http://169.254.169.254/metadata/instance`

## 故障排除

```bash
# 查看 cloud-init 日志
journalctl -u cloud-init-local
cat /var/log/cloud-init.log

# 查看网络配置阶段
cat /var/log/cloud-init-output.log | grep -A 20 "network"

# 检查渲染器是否可用
python3 -c "from cloudinit.net.renderers import netcfg; print(netcfg.available())"

# 手动触发网络配置
cloud-init single --name cc_network
```

## 与 netplan 的区别

| 特性 | netplan | netcfg |
|------|---------|--------|
| 后端 | systemd-networkd / NM | 直接 netlink（无后端服务） |
| init 系统 | 偏向 systemd | init-agnostic（systemd/OpenRC/runit…） |
| systemd-networkd / D-Bus | 需要 | 不需要 |
| netns 支持 | ❌ | ✅ |
| 依赖 | Python + 后端服务 | 单一 Go 二进制 |
| 运行模型 | 渲染后交后端常驻 | 一次性 apply（DHCP 续期用可选 daemon） |
| DHCP | 后端管理 | 内置纯 Go 客户端（外部客户端兜底） |
