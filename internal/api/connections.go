package api

import (
	"net"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/connect"
)

// Connections is the connect.json document the management API edits.
//
// The API talks to the document directly (rather than through the hub's
// read-only view) because it is the writer: create/update/delete must go
// through the same store that seals secrets and keeps the file consistent.
type Connections interface {
	Document() connect.Document
	Replace(incoming connect.Document, server connect.ServerInfo) (connect.Document, error)
	Path() string

	ListAccounts() []connect.Account
	AccountByID(id int64) (connect.Account, bool)
	AccountBySelfID(selfID string) (connect.Account, bool)
	CreateAccount(selfID, name, source string) (connect.Account, error)
	UpdateAccount(account connect.Account) error
	DeleteAccount(id int64) error
	SetAccountToken(id int64, plain, tokenHash, hint string) error
	AccountToken(id int64) (string, error)

	ListBots() []connect.Bot
	BotByID(id int64) (connect.Bot, bool)
	BotByName(name string) (connect.Bot, bool)
	CreateBotWithToken(name, plain, tokenHash, note string) (connect.Bot, error)
	UpdateBot(bot connect.Bot) error
	DeleteBot(id int64) error
	SetBotToken(id int64, plain, tokenHash string) error
	BotToken(id int64) (string, error)

	ListConnections() []connect.Connection
	ConnectionByID(id int64) (connect.Connection, bool)
	ConnectionByName(name string) (connect.Connection, bool)
	CreateConnection(connection connect.Connection, token string) (connect.Connection, error)
	UpdateConnection(connection connect.Connection, token string, replaceToken bool) error
	DeleteConnection(id int64) error
	ConnectionToken(id int64) (string, error)

	ListBindings() []connect.Binding
	BindingsByBot(botName string) []connect.Binding
	BindingsByAccount(selfID string) []connect.Binding
	BindingByID(id int64) (connect.Binding, bool)
	BindingByPair(botName, selfID string) (connect.Binding, bool)
	CreateBinding(binding connect.Binding) (connect.Binding, error)
	UpdateBinding(binding connect.Binding) error
	DeleteBinding(id int64) error
}

// Document exposes the whole connect.json file.
func (s *Server) connectionsDocument() connect.Document {
	if s.opt.Connections == nil {
		return connect.Document{}
	}
	return s.opt.Connections.Document()
}

// connectionStore returns the writer or nil.
func (s *Server) connectionStore() Connections { return s.opt.Connections }

// ServerInfo builds the endpoint summary recorded in the document.
func (s *Server) connectServerInfo() connect.ServerInfo {
	if s.opt.Config == nil {
		return connect.ServerInfo{}
	}
	return connect.ServerInfo{
		UpstreamPath:   s.opt.Config.OneBot.UpstreamWS.Path,
		DownstreamPath: s.opt.Config.OneBot.DownstreamWS.Path,
		HTTPAPIPath:    s.opt.Config.OneBot.HTTP.APIPath,
		HTTPReportPath: s.opt.Config.OneBot.HTTP.ReportPath,
		PublicBase:     publicBase(s.opt.Config.Server.Listen),
	}
}

// publicBase turns a listen address into something an operator can paste into a
// client: the port must survive, and a wildcard bind has to become a real host.
func publicBase(listen string) string {
	if listen == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		// No port at all (or an unparseable address): report it as-is rather than
		// inventing one, so a misconfiguration stays visible.
		return listen
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	if port == "" {
		return host
	}
	return net.JoinHostPort(host, port)
}
