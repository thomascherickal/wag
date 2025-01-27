package router

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/NHAS/wag/internal/config"
	"github.com/NHAS/wag/internal/data"
	"github.com/coreos/go-iptables/iptables"
	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

var (
	lock   sync.RWMutex
	cancel = make(chan bool)
)

func Setup(errorChan chan<- error, iptables bool) (err error) {

	initialUsers, knownDevices, err := data.GetInitialData()
	if err != nil {
		return errors.New("xdp setup get all users and devices: " + err.Error())
	}

	err = setupWireguard(knownDevices)
	if err != nil {
		return err
	}

	if iptables {
		err = setupIptables()
		if err != nil {
			return err
		}
	}

	err = setupXDP(initialUsers, knownDevices)
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			TearDown(true)
		}
	}()

	handleEvents(errorChan)

	go func() {
		startup := true
		cache := map[string]string{}
		d, err := data.GetAllDevices()
		if err != nil {
			errorChan <- err
			return
		}

		for _, device := range d {
			cache[device.Address] = device.Endpoint.String()
		}

		for {

			select {
			case <-cancel:
				return
			case <-time.After(100 * time.Millisecond):
				dev, err := ctrl.Device(config.Values.Wireguard.DevName)
				if err != nil {
					errorChan <- fmt.Errorf("endpoint watcher: %s", err)
					return
				}

				for _, p := range dev.Peers {

					if len(p.AllowedIPs) != 1 {
						log.Println("Warning, peer ", p.PublicKey.String(), " len(p.AllowedIPs) != 1, which is not supported")
						continue
					}

					ip := p.AllowedIPs[0].IP.String()

					if cache[ip] != p.Endpoint.String() {
						cache[ip] = p.Endpoint.String()

						d, err := data.GetDeviceByAddress(ip)
						if err != nil {
							log.Println("unable to get previous device endpoint for", ip, "err:", err)
							if err := Deauthenticate(ip); err != nil {
								log.Println(ip, "unable to remove forwards for device:", err)
							}
							continue
						}

						err = data.UpdateDeviceEndpoint(p.AllowedIPs[0].IP.String(), p.Endpoint)
						if err != nil {
							log.Println(ip, "unable to update device endpoint: ", err)
						}

						//Dont try and remove rules, if we've just started
						if !startup {
							log.Println(ip, "endpoint changed", d.Endpoint.String(), "->", p.Endpoint.String())
							if err := Deauthenticate(ip); err != nil {
								log.Println(ip, "unable to remove forwards for device: ", err)
							}
						}
					}

				}

				startup = false
			}

		}
	}()

	output := []string{"Started firewall management: ",
		"\t\t\tSetting filter FORWARD policy to DROP",
		"\t\t\tXDP eBPF program managing firewall",
		"\t\t\tAllow Iptables FORWARDS to and from wireguard device",
		"\t\t\tAllow input to VPN host"}

	routeMode := "MASQUERADE (NAT)"
	if config.Values.NAT != nil && !*config.Values.NAT {
		routeMode = "RAW (No NAT)"
	}

	output = append(output, "\t\t\tSet routing mode to "+routeMode)

	log.Println(strings.Join(output, "\n"))

	return nil
}

func TearDown(force bool) {

	if !force {
		cancel <- true
	}

	log.Println("Removing wireguard device")
	conn, err := netlink.Dial(unix.NETLINK_ROUTE, nil)
	if err != nil {
		log.Println("Unable to remove wireguard device, netlink connection failed: ", err.Error())
		return
	}
	defer conn.Close()

	err = delWg(conn, config.Values.Wireguard.DevName)
	if err != nil {
		log.Println("Unable to remove wireguard device, delete failed: ", err.Error())
		return
	}

	log.Println("Wireguard device removed")

	log.Println("Removing Firewall rules...")

	ipt, err := iptables.New()
	if err != nil {
		log.Println("Unable to clean up firewall rules: ", err)
		return
	}

	err = ipt.Delete("filter", "FORWARD", "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT")
	if err != nil {
		log.Println("Unable to clean up firewall rules: ", err)
	}

	//Setup the links to the new chains
	err = ipt.Delete("filter", "FORWARD", "-i", config.Values.Wireguard.DevName, "-j", "ACCEPT")
	if err != nil {
		log.Println("Unable to clean up firewall rules: ", err)
	}

	err = ipt.Delete("filter", "FORWARD", "-o", config.Values.Wireguard.DevName, "-j", "ACCEPT")
	if err != nil {
		log.Println("Unable to clean up firewall rules: ", err)
	}

	shouldNAT := config.Values.NAT == nil || (config.Values.NAT != nil && *config.Values.NAT)
	if shouldNAT {
		err = ipt.Delete("nat", "POSTROUTING", "-s", config.Values.Wireguard.Range.String(), "-j", "MASQUERADE")
		if err != nil {
			log.Println("Unable to clean up firewall rules: ", err)
		}
	}

	if config.Values.NumberProxies == 0 {
		//Allow input to authorize web server on the tunnel
		err = ipt.Delete("filter", "INPUT", "-m", "tcp", "-p", "tcp", "-i", config.Values.Wireguard.DevName, "--dport", config.Values.Webserver.Tunnel.Port, "-j", "ACCEPT")
		if err != nil {
			log.Println("Unable to clean up firewall rules: ", err)
		}
	}

	for _, port := range config.Values.ExposePorts {
		parts := strings.Split(port, "/")
		if len(parts) < 2 {
			log.Println(port + " is not in a valid port format. E.g 80/tcp, 100-200/tcp")
		}

		err = ipt.Delete("filter", "INPUT", "-m", parts[1], "-p", parts[1], "-i", config.Values.Wireguard.DevName, "--dport", strings.Replace(parts[0], "-", ":", 1), "-j", "ACCEPT")
		if err != nil {
			log.Println("unable to cleanup custom defined port", port, ":", err)
		}
	}

	err = ipt.Delete("filter", "INPUT", "-p", "icmp", "-i", config.Values.Wireguard.DevName, "-j", "ACCEPT")
	if err != nil {
		log.Println("Unable to clean up firewall rules: ", err)
	}

	err = ipt.Delete("filter", "INPUT", "-i", config.Values.Wireguard.DevName, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT")
	if err != nil {
		log.Println("Unable to clean up firewall rules: ", err)
	}

	err = ipt.Delete("filter", "INPUT", "-i", config.Values.Wireguard.DevName, "-j", "DROP")
	if err != nil {
		log.Println("Unable to clean up firewall rules: ", err)
	}

	log.Println("Firewall rules removed.")

}
