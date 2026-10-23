package main

import (
	"context"
	"errors"
	"os/exec"
	goruntime "runtime"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/jaba0x/switchdesk/internal/core"
	"github.com/jaba0x/switchdesk/internal/firmware"
)

var errNoLibrary = errors.New("the firmware library folder couldn't be opened")

func (a *App) lib() (*firmware.Library, error) {
	if l := a.mgr.Library(); l != nil {
		return l, nil
	}
	return nil, errNoLibrary
}

// FirmwareImages lists the library, picking up files added by hand.
func (a *App) FirmwareImages() ([]firmware.Image, error) {
	l, err := a.lib()
	if err != nil {
		return nil, err
	}
	l.Rescan()
	return l.List(), nil
}

// AddFirmwareFiles copies chosen .swi files into the library.
func (a *App) AddFirmwareFiles() ([]firmware.Image, error) {
	l, err := a.lib()
	if err != nil {
		return nil, err
	}
	paths, err := runtime.OpenMultipleFilesDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   "Add firmware images",
		Filters: []runtime.FileFilter{{DisplayName: "Aruba firmware (*.swi)", Pattern: "*.swi"}},
	})
	if err != nil || len(paths) == 0 {
		return nil, err
	}
	var out []firmware.Image
	var errs []error
	for _, p := range paths {
		img, err := l.AddFile(p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, img)
	}
	a.emit("")
	return out, errors.Join(errs...)
}

var (
	dlMu     sync.Mutex
	dlCancel context.CancelFunc
)

// DownloadFirmware fetches an image from a link and reports progress with
// "firmware-download" events.
func (a *App) DownloadFirmware(link string) (firmware.Image, error) {
	l, err := a.lib()
	if err != nil {
		return firmware.Image{}, err
	}
	ctx, cancel := context.WithCancel(a.ctx)
	dlMu.Lock()
	if dlCancel != nil {
		dlMu.Unlock()
		cancel()
		return firmware.Image{}, errors.New("a download is already running")
	}
	dlCancel = cancel
	dlMu.Unlock()
	defer func() {
		dlMu.Lock()
		dlCancel = nil
		dlMu.Unlock()
		cancel()
	}()
	img, err := l.Download(ctx, link, func(done, total int64) {
		runtime.EventsEmit(a.ctx, "firmware-download", map[string]int64{"done": done, "total": total})
	})
	if err == nil {
		a.emit("")
	}
	if errors.Is(err, context.Canceled) {
		err = errors.New("cancelled")
	}
	return img, err
}

func (a *App) CancelDownload() {
	dlMu.Lock()
	defer dlMu.Unlock()
	if dlCancel != nil {
		dlCancel()
	}
}

func (a *App) DeleteFirmware(file string) error {
	l, err := a.lib()
	if err != nil {
		return err
	}
	err = l.Delete(file)
	a.emit("")
	return err
}

func (a *App) OpenFirmwareFolder() error {
	l, err := a.lib()
	if err != nil {
		return err
	}
	switch goruntime.GOOS {
	case "darwin":
		return exec.Command("open", l.Dir()).Start()
	case "windows":
		return exec.Command("explorer", l.Dir()).Start()
	}
	return exec.Command("xdg-open", l.Dir()).Start()
}

// ---- updates ----

// StartUpgrade queues firmware updates; image "" picks the newest matching one.
func (a *App) StartUpgrade(ids []string, image string, reboot bool) ([]core.Job, error) {
	return a.mgr.StartUpgrade(ids, image, reboot, false)
}

func (a *App) UpgradeJobs() []core.Job { return a.mgr.Jobs() }

func (a *App) CancelUpgrade(id string) error { return a.mgr.CancelJob(id) }

func (a *App) ClearFinishedUpgrades() { a.mgr.ClearFinishedJobs() }
