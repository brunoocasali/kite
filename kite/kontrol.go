package kite

import (
	"errors"
	"fmt"
	"koding/newkite/protocol"
	"net"
	"net/url"
	"sync"
	"time"
)

var ErrNoKitesAvailable = errors.New("no kites availabile")

// Kontrol embeds RemoteKite which has additional special helper methods.
type Kontrol struct {
	*RemoteKite

	// used for synchronizing methods that needs to be called after
	// successful connection.
	ready chan bool
}

// NewKontrol returns a pointer to new Kontrol instance.
func (k *Kite) NewKontrol(kontrolURL *url.URL) *Kontrol {
	// Only the address is required to connect Kontrol
	kite := protocol.Kite{
		Name: "kontrol", // for logging purposes
		URL:  protocol.KiteURL{kontrolURL},
	}

	auth := Authentication{
		Type: "kodingKey",
		Key:  k.KodingKey,
	}

	remoteKite := k.NewRemoteKite(kite, auth)
	remoteKite.client.Reconnect = true

	var once sync.Once
	ready := make(chan bool)

	remoteKite.OnConnect(func() {
		k.Log.Info("Connected to Kontrol ")

		// signal all other methods that are listening on this channel, that we
		// are ready.
		once.Do(func() { close(ready) })
	})

	remoteKite.OnDisconnect(func() { k.Log.Warning("Disconnected from Kontrol. I will retry in background...") })

	return &Kontrol{
		RemoteKite: remoteKite,
		ready:      ready,
	}
}

// Register registers current Kite to Kontrol. After registration other Kites
// can find it via GetKites() method.
func (k *Kontrol) Register() error {
	<-k.ready

	response, err := k.RemoteKite.Tell("register")
	if err != nil {
		return err
	}

	var rr protocol.RegisterResult
	err = response.Unmarshal(&rr)
	if err != nil {
		return err
	}

	switch rr.Result {
	case protocol.AllowKite:
		kite := &k.localKite.Kite

		// we know now which user that is after authentication
		kite.Username = rr.Username

		// Set the correct PublicIP if left empty in options.
		ip, port, _ := net.SplitHostPort(kite.URL.Host)
		if ip == "" {
			kite.URL.Host = net.JoinHostPort(rr.PublicIP, port)
		}

		k.Log.Info("Registered to kontrol with URL: %s version: %s uuid: %s",
			kite.URL.String(), kite.Version, kite.ID)
	case protocol.RejectKite:
		return errors.New("Kite rejected")
	default:
		return fmt.Errorf("Invalid result: %s", rr.Result)
	}

	return nil
}

// WatchKites watches for Kites that matches the query. The onEvent functions
// is called for current kites and every new kite event.
func (k *Kontrol) WatchKites(query protocol.KontrolQuery, onEvent func(*protocol.KiteEvent)) error {
	<-k.ready

	queueEvents := func(r *Request) {
		var event protocol.KiteEvent
		err := r.Args.MustSliceOfLength(1)[0].Unmarshal(&event)
		if err != nil {
			k.Log.Error(err.Error())
			return
		}

		onEvent(&event)
	}

	args := []interface{}{query, Callback(queueEvents)}
	remoteKites, err := k.getKites(args...)
	if err != nil && err != ErrNoKitesAvailable {
		return err // return only when something really happened
	}

	// also put the current kites to the eventChan.
	for _, remoteKite := range remoteKites {
		event := protocol.KiteEvent{
			Action: protocol.Register,
			Kite:   remoteKite.Kite,
			Token: &protocol.Token{
				Key: remoteKite.Authentication.Key,
				TTL: int(remoteKite.Authentication.validUntil.Sub(time.Now().UTC()) / time.Second),
			},
		}

		onEvent(&event)
	}

	return nil
}

// GetKites returns the list of Kites matching the query. The returned list
// contains ready to connect RemoteKite instances. The caller must connect
// with RemoteKite.Dial() before using each Kite. An error is returned when no
// kites are available.
func (k *Kontrol) GetKites(query protocol.KontrolQuery) ([]*RemoteKite, error) {
	return k.getKites(query)
}

// used internally for GetKites() and WatchKites()
func (k *Kontrol) getKites(args ...interface{}) ([]*RemoteKite, error) {
	<-k.ready

	response, err := k.RemoteKite.Tell("getKites", args...)
	if err != nil {
		return nil, err
	}

	var kites []protocol.KiteWithToken
	err = response.Unmarshal(&kites)
	if err != nil {
		return nil, err
	}

	if len(kites) == 0 {
		return nil, ErrNoKitesAvailable
	}

	remoteKites := make([]*RemoteKite, len(kites))
	for i, kite := range kites {
		validUntil := time.Now().UTC().Add(time.Duration(kite.Token.TTL) * time.Second)
		auth := Authentication{
			Type:       "token",
			Key:        kite.Token.Key,
			validUntil: &validUntil,
		}

		remoteKites[i] = k.localKite.NewRemoteKite(kite.Kite, auth)
	}

	return remoteKites, nil
}

// GetToken is used to get a new token for a single Kite.
func (k *Kontrol) GetToken(kite *protocol.Kite) (*protocol.Token, error) {
	<-k.ready

	result, err := k.RemoteKite.Tell("getToken", kite)
	if err != nil {
		return nil, err
	}

	var tkn *protocol.Token
	err = result.Unmarshal(&tkn)
	if err != nil {
		return nil, err
	}

	return tkn, nil
}