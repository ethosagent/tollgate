package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/ethosagent/tollgate"
)

const (
	defaultListen   = ":8080"
	defaultShutdown = 30 * time.Second
)

// overrides are the command-line values that beat the config file. A field is
// used only when its flag was set.
type overrides struct {
	listen, adminListen string
	shutdown            time.Duration
	setListen, setAdmin bool
	setShutdown         bool
}

// prepared is a validated, ready-to-serve configuration.
type prepared struct {
	proxy       *tollgate.Proxy
	listen      string
	adminListen string
	shutdown    time.Duration
	summary     summary
}

// prepare loads the config at path ("" means defaults only), applies ov,
// builds the Options and the Proxy, and returns every problem found at once:
// decode errors, bad values, unset ${VAR}s and Options.Validate / New errors.
// On any problem the returned *prepared is nil.
func prepare(path string, ov overrides, lookup lookupFunc, out io.Writer, logger *slog.Logger) (*prepared, []error) {
	cfg, errs := &fileConfig{}, []error(nil)
	if path != "" {
		cfg, errs = readConfigFile(path)
	}
	c := &collector{errs: errs}
	opts, sum := buildOptions(c, cfg, lookup, out)
	opts.Logger = logger
	pr := &prepared{
		listen:      pick(cfg.Listen, defaultListen, ov.listen, ov.setListen),
		adminListen: pick(cfg.AdminListen, "", ov.adminListen, ov.setAdmin),
		shutdown:    defaultShutdown,
		summary:     sum,
	}
	if d := c.duration("shutdown_timeout", cfg.ShutdownTimeout); d != 0 {
		pr.shutdown = d
	}
	if ov.setShutdown {
		pr.shutdown = ov.shutdown
	}
	checkAddrs(c, pr)
	if pr.shutdown <= 0 {
		c.addf("shutdown_timeout must be positive")
	}
	c.add(flatten(opts.Validate())...)
	if len(c.errs) > 0 {
		return nil, c.errs
	}
	p, err := tollgate.New(opts)
	if err != nil {
		return nil, flatten(err)
	}
	pr.proxy = p
	return pr, nil
}

// pick returns the flag value when set, else the config value, else def.
func pick(cfg, def, flagVal string, set bool) string {
	switch {
	case set:
		return flagVal
	case cfg != "":
		return cfg
	}
	return def
}

func checkAddrs(c *collector, pr *prepared) {
	for _, a := range []struct{ field, addr string }{{"listen", pr.listen}, {"admin_listen", pr.adminListen}} {
		if a.addr == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(a.addr); err != nil {
			c.add(fmt.Errorf("%s: %q is not a host:port address", a.field, a.addr))
		}
	}
}

// flatten splits an errors.Join result into its parts.
func flatten(err error) []error {
	if err == nil {
		return nil
	}
	var j interface{ Unwrap() []error }
	if errors.As(err, &j) {
		return j.Unwrap()
	}
	return []error{err}
}
