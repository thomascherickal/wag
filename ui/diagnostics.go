package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/NHAS/wag/internal/data"
	"github.com/NHAS/wag/internal/router"
)

func firewallDiagnositicsUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.NotFound(w, r)
		return
	}

	_, u := sessionManager.GetSessionFromRequest(r)
	if u == nil {
		http.Redirect(w, r, "/login", http.StatusTemporaryRedirect)
		return
	}

	rules, err := ctrl.FirewallRules()
	if err != nil {
		log.Println("error getting firewall rules data", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	result, err := json.MarshalIndent(rules, "", "    ")
	if err != nil {
		log.Println("error marshalling data", err)
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	d := struct {
		Page
		XDPState string
	}{
		Page: Page{

			Description:  "Firewall state page",
			Title:        "Firewall",
			User:         u.Username,
			WagVersion:   WagVersion,
			ServerID:     serverID,
			ClusterState: clusterState,
		},
		XDPState: string(result),
	}

	err = renderDefaults(w, r, d, "diagnostics/firewall_state.html")

	if err != nil {
		log.Println("unable to render firewall page: ", err)

		w.WriteHeader(http.StatusInternalServerError)
		renderDefaults(w, r, nil, "error.html")
		return
	}
}

func wgDiagnositicsUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.NotFound(w, r)
		return
	}

	_, u := sessionManager.GetSessionFromRequest(r)
	if u == nil {
		http.Redirect(w, r, "/login", http.StatusTemporaryRedirect)
		return
	}

	d := Page{

		Description:  "Wireguard Devices",
		Title:        "wg",
		User:         u.Username,
		WagVersion:   WagVersion,
		ServerID:     serverID,
		ClusterState: clusterState,
	}

	renderDefaults(w, r, d, "diagnostics/wireguard_peers.html")
}

func wgDiagnositicsData(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.NotFound(w, r)
		return
	}

	peers, err := router.ListPeers()
	if err != nil {
		log.Println("unable to list wg peers: ", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := []WgDevicesData{}

	for _, peer := range peers {
		ip := "-"
		if len(peer.AllowedIPs) > 0 {
			ip = peer.AllowedIPs[0].String()
		}

		data = append(data, WgDevicesData{

			ReceiveBytes:  peer.ReceiveBytes,
			TransmitBytes: peer.TransmitBytes,

			PublicKey:         peer.PublicKey.String(),
			Address:           ip,
			EndpointAddress:   peer.Endpoint.String(),
			LastHandshakeTime: peer.LastHandshakeTime.Format(time.RFC1123),
		})
	}

	result, err := json.Marshal(data)
	if err != nil {
		log.Println("unable to marshal peers data: ", err)
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(result)

}

func aclsTest(w http.ResponseWriter, r *http.Request) {
	_, u := sessionManager.GetSessionFromRequest(r)
	if u == nil {
		http.Redirect(w, r, "/login", http.StatusTemporaryRedirect)
		return
	}

	var username string
	switch r.Method {
	case http.MethodPost:
		username = r.PostFormValue("username")
	case http.MethodGet:
		username = ""
	default:
		http.NotFound(w, r)
		return
	}

	acl := ""
	if username != "" {
		b, _ := json.MarshalIndent(data.GetEffectiveAcl(username), "", "    ")
		acl = string(b)
	}

	d := struct {
		Page
		AclString string
		Username  string
	}{
		Page: Page{

			Description:  "ACL Checker",
			Title:        "ACLs",
			User:         u.Username,
			WagVersion:   WagVersion,
			ServerID:     serverID,
			ClusterState: clusterState,
		},
		AclString: acl,
		Username:  username,
	}

	renderDefaults(w, r, d, "diagnostics/acl_tester.html")
}

func firewallCheckTest(w http.ResponseWriter, r *http.Request) {
	_, u := sessionManager.GetSessionFromRequest(r)
	if u == nil {
		http.Redirect(w, r, "/login", http.StatusTemporaryRedirect)
		return
	}

	var inputErrors []error
	address := r.FormValue("address")
	if net.ParseIP(address) == nil {
		inputErrors = append(inputErrors, fmt.Errorf("device (%s) not an ip address", address))
	}

	target := r.FormValue("target")
	targetIP := net.ParseIP(target)
	if targetIP == nil {
		addresses, err := net.LookupIP(target)
		if err != nil {
			inputErrors = append(inputErrors, fmt.Errorf("could not lookup %s, err: %s", target, err))
		} else {
			if len(addresses) == 0 {
				inputErrors = append(inputErrors, fmt.Errorf("no addresses for %s", target))
			} else {
				targetIP = addresses[0]
			}
		}
	}

	proto := r.FormValue("protocol")
	port := 0
	if r.FormValue("port") != "" {
		var err error
		port, err = strconv.Atoi(r.FormValue("port"))
		if err != nil {
			inputErrors = append(inputErrors, fmt.Errorf("could not parse port: %s", err))
		}
	}

	var decision string
	if len(inputErrors) == 0 {
		checkerDecision, err := router.CheckRoute(address, targetIP, proto, port)
		if err != nil {
			decision = err.Error()
		} else {

			isAuthed := "(unauthorised)"
			if router.IsAuthed(address) {
				isAuthed = "(authorised)"
			}

			displayProto := fmt.Sprintf("%d/%s", port, proto)
			if proto == "icmp" {
				displayProto = proto
			}
			decision = fmt.Sprintf("%s -%s-> %s, decided: %s %s", address, displayProto, target, checkerDecision, isAuthed)
		}

	} else {
		decision = errors.Join(inputErrors...).Error()
	}

	d := struct {
		Page
		Address   string
		Target    string
		Port      int
		Decision  string
		Protocols []struct {
			Val      string
			Name     string
			Selected bool
		}
	}{
		Page: Page{

			Description:  "ACL Checker",
			Title:        "ACLs",
			User:         u.Username,
			WagVersion:   WagVersion,
			ServerID:     serverID,
			ClusterState: clusterState,
		},
		Decision: decision,
		Address:  address,
		Port:     port,
		Target:   target,
	}

	d.Protocols = []struct {
		Val      string
		Name     string
		Selected bool
	}{
		{Val: "tcp", Name: "TCP", Selected: proto == "tcp"},
		{Val: "udp", Name: "UDP", Selected: proto == "udp"},
		{Val: "icmp", Name: "ICMP", Selected: proto == "icmp"},
	}

	renderDefaults(w, r, d, "diagnostics/route_checker.html")
}
