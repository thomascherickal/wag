package commands

import (
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/NHAS/wag/pkg/control"
	"github.com/NHAS/wag/pkg/control/wagctl"
)

type webadmin struct {
	fs *flag.FlagSet

	username, password, socket string
	action                     string
}

func Webadmin() *webadmin {
	gc := &webadmin{
		fs: flag.NewFlagSet("webadmin", flag.ContinueOnError),
	}

	gc.fs.StringVar(&gc.username, "username", "", "Admin Username to act upon")
	gc.fs.StringVar(&gc.password, "password", "", "Username to act upon")
	gc.fs.StringVar(&gc.socket, "socket", control.DefaultWagSocket, "Wag instance control socket")

	gc.fs.Bool("add", false, "Add web administrator user (requires -password)")
	gc.fs.Bool("del", false, "Delete admin user")
	gc.fs.Bool("list", false, "List web administration users, if '-username' supply will filter by user")

	gc.fs.Bool("lockaccount", false, "Lock admin account disable login for this web administrator user")
	gc.fs.Bool("unlockaccount", false, "Unlock a web administrator account")

	return gc
}

func (g *webadmin) FlagSet() *flag.FlagSet {
	return g.fs
}

func (g *webadmin) Name() string {

	return g.fs.Name()
}

func (g *webadmin) PrintUsage() {
	g.fs.Usage()
}

func (g *webadmin) Check() error {
	g.fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "lockaccount", "unlockaccount", "del", "list", "add":
			g.action = strings.ToLower(f.Name)
		}
	})

	switch g.action {
	case "del", "unlockaccount", "lockaccount":
		if g.username == "" {
			return errors.New("address must be supplied")
		}
	case "list":
	case "add":
		if g.username == "" || g.password == "" {
			return errors.New("both username and password must be specified to add a user")
		}
	default:
		return errors.New("Unknown flag: " + g.action)
	}

	return nil
}

func (g *webadmin) Run() error {
	ctl := wagctl.NewControlClient(g.socket)

	switch g.action {

	case "add":

		err := ctl.AddAdminUser(g.username, g.password)
		if err != nil {
			return err
		}

		fmt.Println("OK")

	case "del":

		err := ctl.DeleteAdminUser(g.username)
		if err != nil {
			return err
		}

		fmt.Println("OK")

	case "list":

		users, err := ctl.ListAdminUsers(g.username)
		if err != nil {
			return err
		}

		fmt.Println("username,locked,date_added,last_login,ip")
		for _, user := range users {
			fmt.Printf("%s,%s,%s,%s,%s\n", user.Username, user.Locked, user.DateAdded, user.LastLogin, user.IP)
		}
	case "lockaccount":

		err := ctl.LockAdminUser(g.username)
		if err != nil {
			return err
		}

		fmt.Println("OK")

	case "unlockaccount":

		err := ctl.UnlockAdminUser(g.username)
		if err != nil {
			return err
		}

		fmt.Println("OK")
	}

	return nil
}