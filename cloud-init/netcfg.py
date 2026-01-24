"""
cloud-init network renderer for netcfg

This renderer generates netplan-compatible YAML and uses netcfg to apply it.

Installation:
    cp netcfg.py /usr/lib/python3/dist-packages/cloudinit/net/renderers/
    
Configuration (/etc/cloud/cloud.cfg):
    network:
      renderers: ['netcfg', 'netplan', 'eni', 'sysconfig']
"""

import logging
import os
import subprocess
from typing import Optional

from cloudinit.net import renderer
from cloudinit.net.network_state import NetworkState
from cloudinit import subp
from cloudinit import util

LOG = logging.getLogger(__name__)

NETCFG_BINARY = "/usr/sbin/netcfg"
NETCFG_CONFIG_DIR = "/etc/netplan"
NETCFG_CONFIG_FILE = "50-cloud-init.yaml"


class Renderer(renderer.Renderer):
    """Render network configuration using netcfg."""

    def __init__(self, config=None):
        if config is None:
            config = {}
        self.netcfg_path = config.get("netcfg_path", NETCFG_BINARY)
        self.config_dir = config.get("config_dir", NETCFG_CONFIG_DIR)
        super(Renderer, self).__init__(config)

    def render_network_state(
        self,
        network_state: NetworkState,
        templates: Optional[dict] = None,
        target: Optional[str] = None,
    ) -> None:
        """
        Render the network state to netplan-compatible YAML.
        
        Args:
            network_state: The NetworkState object containing network config
            templates: Optional templates (unused)
            target: Optional target root directory
        """
        if target:
            config_dir = os.path.join(target, self.config_dir.lstrip("/"))
        else:
            config_dir = self.config_dir

        # Ensure directory exists
        util.ensure_dir(config_dir)

        # Generate netplan-compatible YAML
        yaml_content = self._render_to_netplan(network_state)
        
        # Write config file
        config_path = os.path.join(config_dir, NETCFG_CONFIG_FILE)
        LOG.debug("Writing netcfg config to %s", config_path)
        util.write_file(config_path, yaml_content)

        # Apply configuration if not targeting a different root
        if not target:
            self._apply_config()

    def _render_to_netplan(self, network_state: NetworkState) -> str:
        """Convert NetworkState to netplan YAML format."""
        import yaml
        
        config = {
            "network": {
                "version": 2,
                "renderer": "netcfg",
            }
        }
        
        ethernets = {}
        bonds = {}
        bridges = {}
        vlans = {}

        for iface in network_state.iter_interfaces():
            iface_type = iface.get("type", "unknown")
            name = iface.get("name")
            
            if not name:
                continue

            iface_cfg = self._render_interface(iface)
            
            if iface_type == "physical":
                ethernets[name] = iface_cfg
            elif iface_type == "bond":
                bonds[name] = iface_cfg
                bonds[name]["interfaces"] = iface.get("bond-slaves", [])
                if iface.get("bond-mode"):
                    bonds[name].setdefault("parameters", {})
                    bonds[name]["parameters"]["mode"] = iface.get("bond-mode")
            elif iface_type == "bridge":
                bridges[name] = iface_cfg
                bridges[name]["interfaces"] = iface.get("bridge_ports", [])
            elif iface_type == "vlan":
                vlans[name] = iface_cfg
                vlans[name]["id"] = iface.get("vlan_id")
                vlans[name]["link"] = iface.get("vlan_link")

        if ethernets:
            config["network"]["ethernets"] = ethernets
        if bonds:
            config["network"]["bonds"] = bonds
        if bridges:
            config["network"]["bridges"] = bridges
        if vlans:
            config["network"]["vlans"] = vlans

        # Add routes
        routes = list(network_state.iter_routes())
        if routes:
            self._add_routes(config, routes)

        # Add DNS
        dns_config = network_state.dns_nameservers
        dns_search = network_state.dns_searchdomains
        if dns_config or dns_search:
            # Add to first interface
            for section in ["ethernets", "bonds", "bridges"]:
                if section in config["network"]:
                    first_iface = list(config["network"][section].keys())[0]
                    config["network"][section][first_iface]["nameservers"] = {}
                    if dns_config:
                        config["network"][section][first_iface]["nameservers"]["addresses"] = dns_config
                    if dns_search:
                        config["network"][section][first_iface]["nameservers"]["search"] = dns_search
                    break

        return yaml.dump(config, default_flow_style=False, sort_keys=False)

    def _render_interface(self, iface: dict) -> dict:
        """Render a single interface configuration."""
        cfg = {}
        
        # MAC address
        if iface.get("mac_address"):
            cfg["match"] = {"macaddress": iface["mac_address"]}
            cfg["set-name"] = iface["name"]

        # DHCP
        subnets = iface.get("subnets", [])
        for subnet in subnets:
            subnet_type = subnet.get("type", "")
            
            if subnet_type == "dhcp4" or subnet_type == "dhcp":
                cfg["dhcp4"] = True
            elif subnet_type == "dhcp6":
                cfg["dhcp6"] = True
            elif subnet_type == "static":
                if "addresses" not in cfg:
                    cfg["addresses"] = []
                address = subnet.get("address")
                prefix = subnet.get("prefix")
                netmask = subnet.get("netmask")
                
                if address:
                    if prefix:
                        cfg["addresses"].append(f"{address}/{prefix}")
                    elif netmask:
                        # Convert netmask to prefix
                        prefix = self._netmask_to_prefix(netmask)
                        cfg["addresses"].append(f"{address}/{prefix}")
                    else:
                        cfg["addresses"].append(address)
                
                # Gateway
                gateway = subnet.get("gateway")
                if gateway:
                    if "." in gateway:
                        cfg["gateway4"] = gateway
                    else:
                        cfg["gateway6"] = gateway
            elif subnet_type == "static6":
                if "addresses" not in cfg:
                    cfg["addresses"] = []
                address = subnet.get("address")
                prefix = subnet.get("prefix", 64)
                if address:
                    cfg["addresses"].append(f"{address}/{prefix}")
                gateway = subnet.get("gateway")
                if gateway:
                    cfg["gateway6"] = gateway
            elif subnet_type == "ipv6_slaac":
                cfg["dhcp6"] = False
                cfg.setdefault("accept-ra", True)

        # MTU
        if iface.get("mtu"):
            cfg["mtu"] = iface["mtu"]

        return cfg

    def _add_routes(self, config: dict, routes: list) -> None:
        """Add routes to configuration."""
        for route in routes:
            network = route.get("network")
            gateway = route.get("gateway")
            interface = route.get("interface")
            metric = route.get("metric")
            
            if not gateway or not interface:
                continue
            
            # Find the interface in config
            for section in ["ethernets", "bonds", "bridges", "vlans"]:
                if section in config["network"] and interface in config["network"][section]:
                    if "routes" not in config["network"][section][interface]:
                        config["network"][section][interface]["routes"] = []
                    
                    route_entry = {"to": network or "default", "via": gateway}
                    if metric:
                        route_entry["metric"] = metric
                    
                    config["network"][section][interface]["routes"].append(route_entry)
                    break

    def _netmask_to_prefix(self, netmask: str) -> int:
        """Convert netmask to CIDR prefix length."""
        return sum(bin(int(x)).count('1') for x in netmask.split('.'))

    def _apply_config(self) -> None:
        """Apply the network configuration using netcfg."""
        if not os.path.exists(self.netcfg_path):
            LOG.warning("netcfg not found at %s, skipping apply", self.netcfg_path)
            return
        
        try:
            LOG.debug("Applying network config with netcfg")
            subp.subp([self.netcfg_path, "apply"], capture=True)
        except subp.ProcessExecutionError as e:
            LOG.error("Failed to apply netcfg config: %s", e)
            raise


def available(target=None) -> bool:
    """
    Check if netcfg renderer is available.
    
    Returns True if netcfg binary exists.
    """
    if target:
        netcfg_path = os.path.join(target, NETCFG_BINARY.lstrip("/"))
    else:
        netcfg_path = NETCFG_BINARY
    
    return os.path.isfile(netcfg_path) and os.access(netcfg_path, os.X_OK)
