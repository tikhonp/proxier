package routers

import (
	"net"
	"regexp"
	"strconv"
	"strings"

	"github.com/tikhonp/proxier/internal/modules/routing/routeros"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/sshx"
)

// Connection is how Proxier reaches a router: the router itself, an optional
// jump host, how the first hop is reached, and the router script's names.
type Connection struct {
	Host     string         `json:"host"`
	Port     int            `json:"port"`
	User     string         `json:"user"`
	JumpHost string         `json:"jump_host"`
	JumpPort int            `json:"jump_port"`
	JumpUser string         `json:"jump_user"`
	Tailnet  bool           `json:"tailnet"`
	Names    routeros.Names `json:"names"`
}

// DefaultUser is the router user the add dialog's commands create.
const DefaultUser = "proxier"

var (
	userRe  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	labelRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
)

// validHost accepts an IP address or a hostname: no scheme, no port.
func validHost(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	if net.ParseIP(h) != nil {
		return true
	}
	for _, l := range strings.Split(strings.TrimSuffix(h, "."), ".") {
		if !labelRe.MatchString(l) {
			return false
		}
	}
	return true
}

// Normalize trims the fields and fills the defaults: port 22, the jump port
// 22 when there is a jump host, the router script's names.
func (c Connection) Normalize() Connection {
	c.Host = strings.TrimSpace(c.Host)
	c.User = strings.TrimSpace(c.User)
	c.JumpHost = strings.TrimSpace(c.JumpHost)
	c.JumpUser = strings.TrimSpace(c.JumpUser)
	c.Names.List = strings.TrimSpace(c.Names.List)
	c.Names.Forwarder = strings.TrimSpace(c.Names.Forwarder)
	if c.Port == 0 {
		c.Port = 22
	}
	if c.User == "" {
		c.User = DefaultUser
	}
	if c.JumpPort == 0 {
		c.JumpPort = 22
	}
	if c.JumpHost == "" {
		c.JumpPort, c.JumpUser = 22, ""
	}
	if c.Names.List == "" {
		c.Names.List = routeros.Defaults.List
	}
	if c.Names.Forwarder == "" {
		c.Names.Forwarder = routeros.Defaults.Forwarder
	}
	return c
}

// Validate checks a normalised connection; the values are i18n keys.
func (c Connection) Validate() store.FieldErrors {
	fe := store.FieldErrors{}
	if !validHost(c.Host) {
		fe["host"] = "routers.err.host"
	}
	if c.Port < 1 || c.Port > 65535 {
		fe["port"] = "routers.err.port"
	}
	if !userRe.MatchString(c.User) {
		fe["user"] = "routers.err.user"
	}
	if c.JumpHost != "" {
		if !validHost(c.JumpHost) {
			fe["jump_host"] = "routers.err.host"
		}
		if c.JumpPort < 1 || c.JumpPort > 65535 {
			fe["jump_port"] = "routers.err.port"
		}
		if !userRe.MatchString(c.JumpUser) {
			fe["jump_user"] = "routers.err.jump_user"
		}
	}
	if !routeros.ValidName(c.Names.List) {
		fe["list_name"] = "routers.err.name"
	}
	if !routeros.ValidName(c.Names.Forwarder) {
		fe["forwarder"] = "routers.err.name"
	}
	if len(fe) == 0 {
		return nil
	}
	return fe
}

// Address is the router's "host:port".
func (c Connection) Address() string {
	return sshx.NormalizeAddress(net.JoinHostPort(c.Host, strconv.Itoa(c.Port)))
}

// JumpAddress is the jump host's "host:port"; "" without one.
func (c Connection) JumpAddress() string {
	if c.JumpHost == "" {
		return ""
	}
	return sshx.NormalizeAddress(net.JoinHostPort(c.JumpHost, strconv.Itoa(c.JumpPort)))
}

// RouterSubject is a router's known-host subject; 0 is the add form's.
func RouterSubject(id int64) string {
	if id == 0 {
		return "router:new"
	}
	return "router:" + strconv.FormatInt(id, 10)
}

// JumpSubject is a jump host's known-host subject.
func JumpSubject(address string) string { return "jump:" + address }

// Target is where sshx connects: the router through its jump host, the
// first hop direct or over the tailnet, every first contact confirmed by the
// admin.
func (c Connection) Target(routerID int64) sshx.Target {
	t := sshx.Target{
		Hop:              sshx.Hop{Address: c.Address(), User: c.User, Subject: RouterSubject(routerID)},
		Network:          sshx.Direct,
		FirstContact:     sshx.ConfirmFirstContact,
		JumpFirstContact: sshx.ConfirmFirstContact,
	}
	if c.Tailnet {
		t.Network = sshx.Tailnet
	}
	if c.JumpHost != "" {
		t.Jump = &sshx.Hop{Address: c.JumpAddress(), User: c.JumpUser, Subject: JumpSubject(c.JumpAddress())}
	}
	return t
}

func connOf(r store.Router) Connection {
	return Connection{
		Host: r.Host, Port: r.Port, User: r.User, JumpHost: r.JumpHost, JumpPort: r.JumpPort, JumpUser: r.JumpUser,
		Tailnet: r.Tailnet, Names: routeros.Names{List: r.AddressList, Forwarder: r.Forwarder},
	}
}
