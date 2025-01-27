package authenticators

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/NHAS/wag/internal/data"
)

// from: https://github.com/duo-labs/webauthn.io/blob/3f03b482d21476f6b9fb82b2bf1458ff61a61d41/server/response.go#L15
func jsonResponse(w http.ResponseWriter, d interface{}, c int) {
	dj, err := json.Marshal(d)
	if err != nil {
		http.Error(w, "Error creating JSON response", http.StatusInternalServerError)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(c)
	fmt.Fprintf(w, "%s", dj)
}

func resultMessage(err error) (string, int) {
	if err == nil {
		return "Success", http.StatusOK
	}

	mail := data.GetHelpMail()

	msg := "Validation failed"
	if strings.Contains(err.Error(), "account is locked") {
		msg = "Account is locked contact: " + mail
	} else if strings.Contains(err.Error(), "device is locked") {
		msg = "Device is locked contact: " + mail
	}
	return msg, http.StatusBadRequest
}

type enable bool

func (e *enable) IsEnabled() bool {
	return bool(*e)
}

func (e *enable) Disable() {
	*e = false
}

func (e *enable) Enable() {
	*e = true
}
