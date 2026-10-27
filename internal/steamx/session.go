// Package steamx keeps the GC session of the one account (token logon, no proxy).
package steamx

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/normo/dota-collector/internal/config"
	dota2 "github.com/paralin/go-dota2"
	devents "github.com/paralin/go-dota2/events"
	steam "github.com/paralin/go-steam"
	sp "github.com/paralin/go-steam/protocol"
	"github.com/paralin/go-steam/protocol/protobuf"
	"github.com/paralin/go-steam/protocol/steamlang"
	"github.com/sirupsen/logrus"
)

type LoginError struct{ Status, Guard, Message string }

func (e *LoginError) Error() string { return e.Message }

// ErrPlayingElsewhere: the owner is in a game on this account; announcing Dota would kick them.
var ErrPlayingElsewhere = errors.New("account is playing elsewhere; GC postponed")

type Session struct {
	Client *steam.Client
	Dota   *dota2.Dota2
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// Open logs on with the saved token and returns once the Dota GC has welcomed the session.
func Open(ctx context.Context, username, token string, cfg config.GC) (*Session, error) {
	run, cancel := context.WithCancel(ctx)
	s := &Session{Client: steam.NewClient(), ctx: run, cancel: cancel, done: make(chan struct{})}
	cctx, stop := context.WithTimeout(run, cfg.WelcomeTimeout.Duration)
	_, err := s.Client.ConnectContext(cctx)
	stop()
	if err != nil {
		cancel()
		return nil, errors.New("Steam CM connection failed")
	}
	ready := make(chan error, 1)
	signal := func(err error) {
		select {
		case ready <- err:
		default:
		}
	}
	go func() {
		defer close(s.done)
		defer cancel()
		defer s.Client.Disconnect()
		defer func() {
			if recover() != nil {
				signal(errors.New("Steam event loop panic"))
			}
		}()
		welcomed := false
		hello := time.NewTicker(5 * time.Second)
		defer hello.Stop()
		// Steam reports the playing state right after logon; without it we start after a short wait.
		var announce <-chan time.Time
		start := func() {
			announce = nil
			if s.Dota != nil {
				return
			}
			logger := logrus.New()
			logger.SetOutput(io.Discard)
			s.Dota = dota2.New(s.Client, logger)
			s.Dota.SetPlaying(true)
			s.Dota.SayHello()
		}
		for {
			select {
			case <-run.Done():
				return
			case <-announce:
				start()
			case <-hello.C:
				if s.Dota != nil && !welcomed {
					s.Dota.SayHello()
				}
			case event := <-s.Client.Events():
				switch e := event.(type) {
				case *steam.ConnectedEvent:
					if err := s.Client.Auth.LogOn(run, &steam.LogOnDetails{Username: username, AccessToken: token}); err != nil {
						signal(errors.New("Steam token logon failed"))
						return
					}
				case *steam.LoggedOnEvent:
					if e.Result != steamlang.EResult_OK {
						signal(classifyLogin(e.Result))
						return
					}
					announce = time.After(5 * time.Second)
				case *steam.PlayingSessionStateEvent:
					if e.PlayingBlocked {
						signal(ErrPlayingElsewhere)
						return
					}
					start()
				case *devents.ClientWelcomed:
					if !welcomed {
						welcomed = true
						signal(nil)
					}
				case *steam.LogOnFailedEvent:
					signal(classifyLogin(e.Result))
					return
				case *steam.DisconnectedEvent, *steam.LoggedOffEvent, steam.FatalErrorEvent:
					signal(errors.New("Steam disconnected"))
					return
				}
			}
		}
	}()
	timer := time.NewTimer(cfg.WelcomeTimeout.Duration)
	defer timer.Stop()
	select {
	case err := <-ready:
		if err != nil {
			s.Close()
			return nil, err
		}
		return s, nil
	case <-timer.C:
		s.Close()
		return nil, errors.New("Dota GC welcome timeout")
	case <-run.Done():
		s.Close()
		return nil, errors.New("Steam connection ended before GC welcome")
	}
}

func (s *Session) Alive() bool { return s.ctx.Err() == nil }

func (s *Session) Close() {
	if s.Client.Connected() {
		if s.Dota != nil {
			s.Dota.SetPlaying(false)
		}
		s.Client.Write(sp.NewClientMsgProtobuf(steamlang.EMsg_ClientLogOff, &protobuf.CMsgClientLogOff{}))
	}
	s.cancel()
	s.Client.Disconnect()
	<-s.done
}
