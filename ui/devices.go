package ui

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/NHAS/wag/internal/data"
)

func devicesMgmtUI(w http.ResponseWriter, r *http.Request) {
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

		Description:  "Devices Management Page",
		Title:        "Devices",
		User:         u.Username,
		WagVersion:   WagVersion,
		ServerID:     serverID,
		ClusterState: clusterState,
	}

	err := renderDefaults(w, r, d, "management/devices.html", "delete_modal.html")

	if err != nil {
		log.Println("unable to render devices page: ", err)

		w.WriteHeader(http.StatusInternalServerError)
		renderDefaults(w, r, nil, "error.html")
		return
	}
}

func devicesMgmt(w http.ResponseWriter, r *http.Request) {

	switch r.Method {
	case "GET":
		allDevices, err := ctrl.ListDevice("")
		if err != nil {
			log.Println("error getting devices: ", err)

			w.WriteHeader(http.StatusInternalServerError)
			renderDefaults(w, r, nil, "error.html")
			return
		}

		lockout, err := data.GetLockout()
		if err != nil {
			log.Println("error getting lockout: ", err)

			w.WriteHeader(http.StatusInternalServerError)
			renderDefaults(w, r, nil, "error.html")
			return
		}

		data := []DevicesData{}

		for _, dev := range allDevices {
			data = append(data, DevicesData{
				Owner:        dev.Username,
				Locked:       dev.Attempts >= lockout,
				InternalIP:   dev.Address,
				PublicKey:    dev.Publickey,
				LastEndpoint: dev.Endpoint.String(),
				Active:       dev.Active,
			})
		}

		b, err := json.Marshal(data)
		if err != nil {

			log.Println("unable to marshal devices data: ", err)
			http.Error(w, "Server error", 500)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	case "PUT":
		var action struct {
			Action    string   `json:"action"`
			Addresses []string `json:"addresses"`
		}

		err := json.NewDecoder(r.Body).Decode(&action)
		if err != nil {
			http.Error(w, "Bad request", 400)
			return
		}

		for _, address := range action.Addresses {
			switch action.Action {
			case "lock":
				err := ctrl.LockDevice(address)
				if err != nil {
					log.Println("Error locking device: ", address, " err:", err)
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
			case "unlock":
				err := ctrl.UnlockDevice(address)
				if err != nil {
					log.Println("Error unlocking device: ", address, " err:", err)
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
			default:
				http.Error(w, "invalid action", 400)
				return
			}
		}

		w.Write([]byte("OK"))

	case "DELETE":
		var addresses []string

		err := json.NewDecoder(r.Body).Decode(&addresses)
		if err != nil {
			http.Error(w, "Bad request", 400)
			return
		}

		for _, address := range addresses {
			err := ctrl.DeleteDevice(address)
			if err != nil {
				log.Println("Error Deleting device: ", address, "err:", err)

				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		w.Write([]byte("OK"))

	default:
		http.NotFound(w, r)
	}

}
