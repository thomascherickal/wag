package router

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"
	"unsafe"

	"github.com/NHAS/wag/config"
	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"

	"github.com/NHAS/wag/database"
	"github.com/NHAS/wag/utils"

	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

var (
	ctrl *wgctrl.Client
)

type IfInfomsg struct {
	Family uint8
	_      uint8
	Type   uint16
	Index  int32
	Flags  uint32
	Change uint32
}

func (msg *IfInfomsg) Serialize() []byte {
	return (*(*[unix.SizeofIfInfomsg]byte)(unsafe.Pointer(msg)))[:]
}

type IfAddrmsg struct {
	Family    uint8
	Prefixlen uint8
	Flags     uint8
	Scope     uint8
	Index     uint32
}

func (msg *IfAddrmsg) Serialize() []byte {
	return (*(*[unix.SizeofIfAddrmsg]byte)(unsafe.Pointer(msg)))[:]
}

func setupWireguard() error {

	c := wgtypes.Config{
		ReplacePeers: true,
	}

	if !config.Values().Wireguard.External {

		conn, err := netlink.Dial(unix.NETLINK_ROUTE, nil)
		if err != nil {
			return err
		}
		defer conn.Close()

		ip, network, err := net.ParseCIDR(config.Values().Wireguard.Address)
		if err != nil {
			return err
		}
		network.IP = ip.To4()[:4] // Stop netlink freaking out at a ipv6 length ipv4 address

		err = addWg(conn, config.Values().Wireguard.DevName, *network, config.Values().Wireguard.MTU)
		if err != nil {
			return err
		}

		key, err := wgtypes.ParseKey(config.Values().Wireguard.PrivateKey)
		if err != nil {
			return err
		}
		c.PrivateKey = &key

		port := config.Values().Wireguard.ListenPort
		c.ListenPort = &port
	}

	devices, err := database.GetDevices()
	if err != nil {
		return err
	}

	for _, device := range devices {
		pk, _ := wgtypes.ParseKey(device.Publickey)
		keepalive := time.Duration(time.Duration(config.Values().Wireguard.PersistentKeepAlive)) * time.Second

		_, network, _ := net.ParseCIDR(device.Address + "/32")

		c.Peers = append(c.Peers, wgtypes.PeerConfig{
			PublicKey:                   pk,
			PersistentKeepaliveInterval: &keepalive,
			ReplaceAllowedIPs:           true,
			AllowedIPs:                  []net.IPNet{*network},
		})
	}

	ctrl, err = wgctrl.New()
	if err != nil {
		return fmt.Errorf("cannot start wireguard control %v", err)
	}

	err = ctrl.ConfigureDevice(config.Values().Wireguard.DevName, c)
	if err != nil {
		return fmt.Errorf("cannot configure wireguard device %v", err)

	}

	return nil
}

func ServerDetails() (key wgtypes.Key, port int, err error) {
	ctr, err := wgctrl.New()
	if err != nil {
		return key, port, fmt.Errorf("cannot start wireguard control %v", err)
	}
	defer ctr.Close()

	dev, err := ctr.Device(config.Values().Wireguard.DevName)
	if err != nil {
		return key, port, fmt.Errorf("unable to start wireguard-ctrl on device with name %s: %v", config.Values().Wireguard.DevName, err)
	}

	return dev.PublicKey, dev.ListenPort, nil
}

func RemovePeer(internalAddress string) error {

	dev, err := ctrl.Device(config.Values().Wireguard.DevName)
	if err != nil {
		return err
	}

	var pubkey wgtypes.Key
	found := false
	for _, peer := range dev.Peers {
		if len(peer.AllowedIPs) == 1 && peer.AllowedIPs[0].IP.String() == internalAddress {
			pubkey = peer.PublicKey
			found = true
			break
		}
	}

	if !found {
		return errors.New("wireguard peer not found")
	}

	var c wgtypes.Config
	c.Peers = append(c.Peers, wgtypes.PeerConfig{
		PublicKey: pubkey,
		Remove:    true,
	})

	// Try both
	err1 := ctrl.ConfigureDevice(config.Values().Wireguard.DevName, c)
	err2 := xdpRemoveDevice(internalAddress)

	if err1 != nil {
		return err1
	}

	if err2 != nil {
		return err1
	}

	return nil
}

// AddPeer the device to wireguard
func AddPeer(public wgtypes.Key, username string) (string, error) {

	dev, err := ctrl.Device(config.Values().Wireguard.DevName)
	if err != nil {
		return "", err
	}

	//Poor selection algorithm

	//If we dont have any peers take the server tun address and increment that
	newAddress := config.Values().Wireguard.ServerAddress.String()
	if len(dev.Peers) > 0 {
		addresses := []string{}
		for _, peer := range dev.Peers {
			addresses = append(addresses, utils.GetIP(peer.AllowedIPs[0].IP.String()))
		}

		newAddress = addresses[len(addresses)-1]
	}

	newAddress, err = incrementIP(newAddress, config.Values().Wireguard.Range.String())
	if err != nil {
		return "", err
	}

	_, network, err := net.ParseCIDR(newAddress + "/32")
	if err != nil {
		return "", err
	}

	var c wgtypes.Config
	c.Peers = append(c.Peers, wgtypes.PeerConfig{
		PublicKey:         public,
		ReplaceAllowedIPs: true,
		AllowedIPs:        []net.IPNet{*network},
	})

	newDevice, err := database.CreateMFAEntry(newAddress, public.String(), username)
	if err != nil {
		return "", errors.New("unable to setup for first use mfa: " + err.Error())
	}

	err = xdpAddDevice(newDevice)
	if err != nil {

		//make sure we attempt to clean up the db if the xdp add fails
		database.DeleteDevice(newAddress)

		return "", err
	}

	return network.IP.String(), ctrl.ConfigureDevice(config.Values().Wireguard.DevName, c)
}

func GetPeerRealIp(address string) (string, error) {
	dev, err := ctrl.Device(config.Values().Wireguard.DevName)
	if err != nil {
		return "", err
	}

	for _, peer := range dev.Peers {
		if len(peer.AllowedIPs) == 1 && peer.AllowedIPs[0].IP.String() == address {
			return peer.Endpoint.String(), nil
		}
	}

	return "", errors.New("not found")
}

func incrementIP(origIP, cidr string) (string, error) {
	ip := net.ParseIP(origIP)
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return origIP, err
	}
	for i := len(ip) - 1; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			break
		}
	}
	if !ipNet.Contains(ip) {
		return origIP, errors.New("overflowed CIDR while incrementing IP")
	}
	return ip.String(), nil
}

func addWg(c *netlink.Conn, name string, address net.IPNet, mtu int) error {

	infomsg := IfInfomsg{
		Family: unix.AF_UNSPEC,
		Change: unix.IFF_UP | unix.IFF_LOWER_UP,
		Flags:  unix.IFF_UP | unix.IFF_LOWER_UP,
	}

	ne := netlink.NewAttributeEncoder()
	ne.Int32(unix.IFLA_MTU, int32(mtu))
	ne.String(unix.IFLA_IFNAME, name)

	ne.Nested(unix.IFLA_LINKINFO, func(nae *netlink.AttributeEncoder) error {
		nae.String(unix.IFLA_INFO_KIND, unix.WG_GENL_NAME)
		return nil
	})

	req := netlink.Message{
		Header: netlink.Header{
			Type:  unix.RTM_NEWLINK,
			Flags: netlink.Request | netlink.Create | netlink.Excl | netlink.Acknowledge,
		},
	}

	req.Data = infomsg.Serialize()

	msg, err := ne.Encode()
	if err != nil {
		return fmt.Errorf("failed to encode: %v", err)
	}

	req.Data = append(req.Data, msg...)

	resp, err := c.Execute(req)
	if err != nil {
		return fmt.Errorf("failed to execute message: %v", err)
	}

	switch resp[0].Header.Type {
	case netlink.Error:
		errCode := binary.LittleEndian.Uint32(resp[0].Data)
		if errCode != 0 {
			fmt.Println("Netlink reported error: ", errCode)
			return errors.New("got netlink error: " + fmt.Sprintf("%d", errCode))
		}
	}

	return setIp(c, name, address)
}

func setIp(c *netlink.Conn, name string, address net.IPNet) error {

	req := netlink.Message{
		Header: netlink.Header{
			Type:  unix.RTM_NEWADDR,
			Flags: netlink.Request | netlink.Acknowledge,
		},
	}

	iface, err := net.InterfaceByName(name)
	if err != nil {
		return fmt.Errorf("wireguard network iface %s does not exist: %s", name, err)
	}

	addrMsg := IfAddrmsg{
		Family: unix.AF_INET,
		Index:  uint32(iface.Index),
	}

	preflen, _ := address.Mask.Size()
	addrMsg.Prefixlen = uint8(preflen)

	req.Data = addrMsg.Serialize()

	ne := netlink.NewAttributeEncoder()
	ne.Bytes(unix.IFA_LOCAL, address.IP[:4])

	msg, err := ne.Encode()
	if err != nil {
		return fmt.Errorf("failed to encode af: %v", err)
	}

	req.Data = append(req.Data, msg...)

	resp, err := c.Execute(req)
	if err != nil {
		return fmt.Errorf("failed to execute message: %v", err)
	}

	switch resp[0].Header.Type {
	case netlink.Error:
		errCode := binary.LittleEndian.Uint32(resp[0].Data)
		if errCode != 0 {
			fmt.Println("Netlink reported error: ", errCode)
			return errors.New("got netlink error: " + fmt.Sprintf("%d", errCode))
		}
	}

	return nil
}

func delWg(c *netlink.Conn, name string) error {
	infomsg := IfInfomsg{
		Family: unix.AF_UNSPEC,
		Change: unix.IFF_UP | unix.IFF_LOWER_UP,
		Flags:  unix.IFF_UP | unix.IFF_LOWER_UP,
	}

	ne := netlink.NewAttributeEncoder()
	ne.String(unix.IFLA_IFNAME, name)

	ne.Nested(unix.IFLA_LINKINFO, func(nae *netlink.AttributeEncoder) error {
		nae.String(unix.IFLA_INFO_KIND, unix.WG_GENL_NAME)
		return nil
	})

	req := netlink.Message{
		Header: netlink.Header{
			Type:  unix.RTM_DELLINK,
			Flags: netlink.Request | netlink.Acknowledge,
		},
	}

	req.Data = infomsg.Serialize()

	msg, err := ne.Encode()
	if err != nil {
		return fmt.Errorf("failed to encode: %v", err)
	}

	req.Data = append(req.Data, msg...)

	resp, err := c.Execute(req)
	if err != nil {
		return fmt.Errorf("failed to execute message: %v", err)
	}

	switch resp[0].Header.Type {
	case netlink.Error:
		errCode := binary.LittleEndian.Uint32(resp[0].Data)
		if errCode != 0 {
			fmt.Println("Netlink reported error: ", errCode)
			return errors.New("got netlink error: " + fmt.Sprintf("%d", errCode))
		}
	}

	return nil
}
