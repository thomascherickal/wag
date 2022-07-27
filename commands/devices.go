package commands

import (
	"errors"
	"flag"
	"fmt"
	"strings"
	"wag/config"
	"wag/control"
	"wag/database"
)

type devices struct {
	fs *flag.FlagSet

	device string
	action string

	config config.Config
}

func Devices() *devices {
	gc := &devices{
		fs: flag.NewFlagSet("devices", flag.ContinueOnError),
	}

	gc.fs.StringVar(&gc.device, "device", "", "Device address")

	gc.fs.Bool("del", false, "Completely remove device blocks wireguard access")
	gc.fs.Bool("list", false, "List devices with 2fa entries")
	gc.fs.Bool("sessions", false, "Get list of currently active authorised sessions")

	gc.fs.Bool("reset", false, "Reset locked account/device")
	gc.fs.Bool("lock", false, "Locked account/device access to mfa routes")

	return gc
}

func (g *devices) Name() string {

	return g.fs.Name()
}

func (g *devices) PrintUsage() {
	g.fs.Usage()
}

func (g *devices) Init(args []string, config config.Config) error {
	err := g.fs.Parse(args)
	if err != nil {
		return err
	}

	g.config = config

	g.fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "reset", "del", "list", "lock", "sessions":
			g.action = strings.ToLower(f.Name)
		}
	})

	g.action = strings.ToLower(g.action)

	switch g.action {
	case "del", "reset", "lock":
		if g.device == "" {
			return errors.New("Device must be supplied")
		}
	case "list", "sessions":
	default:
		return errors.New("Invalid action choice")
	}

	err = database.Load(config.DatabaseLocation, config.Issuer, config.Lockout)
	if err != nil {
		return fmt.Errorf("Cannot load database: %v", err)
	}

	return nil

}

func (g *devices) Run() error {
	switch g.action {
	case "del":

		err := database.DeleteDevice(g.device)
		if err != nil {
			return errors.New("Could not delete token: " + err.Error())
		}

		err = control.Block(g.device)
		if err != nil {
			return err
		}

		fmt.Println("OK")
	case "list":
		result, err := database.GetDevices()
		if err != nil {
			return err
		}

		fmt.Println("username,address,publickey,enforcingmfa,authattempts")
		for address, properties := range result {
			fmt.Printf("%s,%s,%s,%t,%d\n", properties.Username, address, properties.Publickey, properties.Enforcing, properties.Attempts)
		}
	case "sessions":
		sessions, err := control.Sessions()
		if err != nil {
			return err
		}
		fmt.Println("vpn_address,actual_endpoint")
		fmt.Println(sessions)
	case "lock":

		err := database.SetAttemptsLeft(g.device, g.config.Lockout+1)
		if err != nil {
			return errors.New("Could not lock device: " + err.Error())
		}

		err = control.Block(g.device)
		if err != nil {
			return err
		}

		fmt.Println("OK")

	case "reset":
		err := database.SetAttemptsLeft(g.device, 0)
		if err != nil {
			return errors.New("Could not reset device authentication attempts: " + err.Error())
		}
		fmt.Println("OK")
	}

	return nil
}