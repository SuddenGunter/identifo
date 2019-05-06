package model

import (
	"encoding/json"
	"time"
)

// Session is a session.
type Session struct {
	ID             string
	ExpirationDate time.Time
}

// SessionDuration wraps time.Duration to implement custom yaml and json encoding and decoding.
type SessionDuration struct {
	time.Duration
}

// MarshalJSON implements json.Marshaller.
func (sd SessionDuration) MarshalJSON() ([]byte, error) {
	return json.Marshal(sd.Duration / time.Second)
}

// UnmarshalJSON implements json.Unmarshaller.
func (sd *SessionDuration) UnmarshalJSON(data []byte) error {
	if sd == nil {
		return nil
	}

	var aux int
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	sd.Duration = time.Second * time.Duration(aux)
	return nil
}

// MarshalYAML implements yaml.Marshaller.
func (sd SessionDuration) MarshalYAML() (interface{}, error) {
	return int(sd.Duration / time.Second), nil
}

// UnmarshalYAML implements yaml.Unmarshaller.
func (sd *SessionDuration) UnmarshalYAML(unmarshal func(interface{}) error) error {
	if sd == nil {
		return nil
	}

	var aux int
	if err := unmarshal(&aux); err != nil {
		return err
	}

	*sd = SessionDuration{Duration: time.Second * time.Duration(aux)}
	return nil
}