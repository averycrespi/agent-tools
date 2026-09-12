//go:build linux

package keyring

import (
	"context"
	"errors"
	"os"

	"github.com/godbus/dbus/v5"
)

const (
	secretServiceName      = "org.freedesktop.secrets"
	secretServicePath      = dbus.ObjectPath("/org/freedesktop/secrets")
	secretCollectionPath   = dbus.ObjectPath("/org/freedesktop/secrets/aliases/default")
	secretServiceInterface = "org.freedesktop.Secret.Service"
)

func probeSystem(ctx context.Context, _ string) error {
	return probeLinux(ctx, os.Getenv("DBUS_SESSION_BUS_ADDRESS"), connectLinuxSecretService)
}

type dbusSecretServiceProbe struct {
	connection *dbus.Conn
	prompt     dbus.ObjectPath
}

func connectLinuxSecretService() (linuxProbeClient, error) {
	connection, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, err
	}
	return &dbusSecretServiceProbe{connection: connection}, nil
}

func (probe *dbusSecretServiceProbe) HasOwner(ctx context.Context) (bool, error) {
	var hasOwner bool
	err := probe.connection.BusObject().CallWithContext(
		ctx,
		"org.freedesktop.DBus.NameHasOwner",
		0,
		secretServiceName,
	).Store(&hasOwner)
	return hasOwner, err
}

func (probe *dbusSecretServiceProbe) Locked(ctx context.Context) (bool, error) {
	collection := probe.connection.Object(secretServiceName, secretCollectionPath)
	var lockedVariant dbus.Variant
	err := collection.CallWithContext(
		ctx,
		"org.freedesktop.DBus.Properties.Get",
		0,
		"org.freedesktop.Secret.Collection",
		"Locked",
	).Store(&lockedVariant)
	if err != nil {
		if missingSecretCollection(err) {
			return false, errSecretCollectionAbsent
		}
		return false, err
	}
	locked, ok := lockedVariant.Value().(bool)
	if !ok {
		return false, errors.New("credential backend returned an invalid lock state")
	}
	return locked, nil
}

func (probe *dbusSecretServiceProbe) UnlockWithoutPrompt(ctx context.Context) (bool, bool, error) {
	var unlocked []dbus.ObjectPath
	service := probe.connection.Object(secretServiceName, secretServicePath)
	err := service.CallWithContext(
		ctx,
		secretServiceInterface+".Unlock",
		0,
		[]dbus.ObjectPath{secretCollectionPath},
	).Store(&unlocked, &probe.prompt)
	if err != nil {
		return false, false, err
	}
	for _, path := range unlocked {
		if path == secretCollectionPath {
			return true, false, nil
		}
	}
	return false, probe.prompt.IsValid() && probe.prompt != "/", nil
}

func (probe *dbusSecretServiceProbe) DismissPrompt(ctx context.Context) error {
	if !probe.prompt.IsValid() || probe.prompt == "/" {
		return nil
	}
	return probe.connection.Object(secretServiceName, probe.prompt).CallWithContext(
		ctx,
		"org.freedesktop.Secret.Prompt.Dismiss",
		0,
	).Err
}

func (probe *dbusSecretServiceProbe) Close() error {
	return probe.connection.Close()
}

func missingSecretCollection(err error) bool {
	var dbusErr dbus.Error
	if !errors.As(err, &dbusErr) {
		return false
	}
	return dbusErr.Name == "org.freedesktop.DBus.Error.UnknownObject" ||
		dbusErr.Name == "org.freedesktop.Secret.Error.NoSuchObject"
}
