package replays

import (
	"fmt"
	"net"
	"net/url"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/lang"
	"fyne.io/fyne/v2/widget"
	"github.com/owlcms/video/internal/access"
	replayscfg "github.com/owlcms/video/internal/config/replays"
	"github.com/owlcms/video/internal/logging"
)

var refreshAccessDialog func()

func accessStatusText(service *access.Service) string {
	state, viewers := service.ViewerState()
	if state == access.StateUnavailable {
		return lang.Localize("Access: token file unreadable - all viewers refused")
	}
	if state == access.StateOpen {
		return lang.Localize("Access: open (no token)")
	}
	count := 0
	var next time.Time
	for _, viewer := range viewers {
		if time.Now().Before(viewer.ExpiresAt) {
			count++
			if next.IsZero() || viewer.ExpiresAt.Before(next) {
				next = viewer.ExpiresAt
			}
		}
	}
	if count == 0 {
		return lang.Localize("Access: token required - no valid token, all viewers refused")
	}
	return fmt.Sprintf(lang.Localize("Access: token required - %d valid, next expiry %s"),
		count, next.Local().Format(time.DateTime))
}

func showAccessDialog(window fyne.Window, cfg *replayscfg.Config, service *access.Service) {
	name := widget.NewEntry()
	name.SetPlaceHolder(lang.Localize("Token name"))
	rows := container.NewVBox()
	status := widget.NewLabel("")
	errorLabel := widget.NewLabel("")
	var busy bool
	var refresh func()
	var content *fyne.Container
	var scroll *container.Scroll

	run := func(action func() (string, access.Viewer, error)) {
		if busy {
			return
		}
		busy = true
		refresh()
		go func() {
			token, viewer, err := action()
			fyne.Do(func() {
				busy = false
				if err != nil {
					errorLabel.SetText(lang.Localize(err.Error()))
				} else {
					errorLabel.SetText("")
					if token != "" {
						name.SetText("")
						showGeneratedToken(window, cfg, viewer, token)
					}
				}
				refresh()
			})
		}()
	}

	add := widget.NewButton(lang.Localize("Add token"), func() {
		tokenName := name.Text
		confirm := func(ok bool) {
			if ok {
				run(func() (string, access.Viewer, error) {
					value, viewer, err := service.AddViewer(tokenName)
					if err == nil {
						logging.InfoLogger.Printf("Replays access token %q added, expires %s", viewer.Name, viewer.ExpiresAt)
					}
					return value, viewer, err
				})
			}
		}
		state, _ := service.ViewerState()
		switch state {
		case access.StateOpen:
			dialog.ShowConfirm(lang.Localize("Enable token access"),
				lang.Localize("Viewers without a token, including OBS and Tracker, will be refused."), confirm, window)
		case access.StateUnavailable:
			dialog.ShowConfirm(lang.Localize("Replace unreadable access file"),
				lang.Localize("The unreadable access file will be replaced and existing tokens will be lost."), confirm, window)
		default:
			confirm(true)
		}
	})
	open := widget.NewButton(lang.Localize("Open access"), func() {
		dialog.ShowConfirm(lang.Localize("Open access"),
			lang.Localize("All tokens will be deleted. Anyone who can reach the server can access replays."), func(ok bool) {
				if ok {
					run(func() (string, access.Viewer, error) {
						err := service.OpenViewerAccess()
						if err == nil {
							logging.InfoLogger.Print("Replays access opened, all tokens deleted")
						}
						return "", access.Viewer{}, err
					})
				}
			}, window)
	})
	refresh = func() {
		state, viewers := service.ViewerState()
		status.SetText(accessStatusText(service))
		rows.RemoveAll()
		for _, v := range viewers {
			viewer := v
			expiry := viewer.ExpiresAt.Local().Format(time.DateTime)
			if !time.Now().Before(viewer.ExpiresAt) {
				expiry = lang.Localize("EXPIRED") + " " + expiry
			}
			label := widget.NewLabel(fmt.Sprintf("%s | %s | %s", viewer.Name, viewer.CreatedAt.Local().Format(time.DateTime), expiry))
			regenerate := widget.NewButton(lang.Localize("Regenerate"), func() {
				dialog.ShowConfirm(lang.Localize("Regenerate token"),
					fmt.Sprintf(lang.Localize("%s's current token and browser sessions will stop working."), viewer.Name),
					func(ok bool) {
						if ok {
							run(func() (string, access.Viewer, error) {
								value, updated, err := service.RegenerateViewer(viewer.ID)
								if err == nil {
									logging.InfoLogger.Printf("Replays access token %q regenerated, expires %s", updated.Name, updated.ExpiresAt)
								}
								return value, updated, err
							})
						}
					}, window)
			})
			revoke := widget.NewButton(lang.Localize("Revoke"), func() {
				message := fmt.Sprintf(lang.Localize("Revoke %s's token?"), viewer.Name)
				if len(viewers) == 1 {
					message += "\n" + lang.Localize("This is the last token. Replays will become accessible without a token.")
				}
				dialog.ShowConfirm(lang.Localize("Revoke token"), message, func(ok bool) {
					if ok {
						run(func() (string, access.Viewer, error) {
							err := service.RevokeViewer(viewer.ID)
							if err == nil {
								logging.InfoLogger.Printf("Replays access token %q revoked", viewer.Name)
							}
							return "", access.Viewer{}, err
						})
					}
				}, window)
			})
			if busy {
				regenerate.Disable()
				revoke.Disable()
			}
			rows.Add(container.NewBorder(nil, nil, nil, container.NewHBox(regenerate, revoke), label))
		}
		if busy {
			add.Disable()
			open.Disable()
		} else {
			add.Enable()
			if state == access.StateOpen {
				open.Disable()
			} else {
				open.Enable()
			}
		}
		// Adding rows only lays out the rows box within its previous size;
		// the enclosing box and scroll must be laid out again.
		if scroll != nil {
			content.Refresh()
			content.Resize(content.MinSize().Max(scroll.Size()))
			scroll.Refresh()
		}
	}
	refreshAccessDialog = refresh
	refresh()
	content = container.NewVBox(status, rows, name, errorLabel, container.NewHBox(add, open))
	scroll = container.NewVScroll(content)
	d := dialog.NewCustom(lang.Localize("Replays access tokens"), lang.Localize("Close"), scroll, window)
	d.Resize(fyne.NewSize(760, 420))
	d.SetOnClosed(func() { refreshAccessDialog = nil })
	d.Show()
}

func showGeneratedToken(window fyne.Window, cfg *replayscfg.Config, viewer access.Viewer, token string) {
	lan := "http://" + net.JoinHostPort(getReplayListHost(), fmt.Sprint(cfg.Port))
	q := url.Values{"token": []string{token}}.Encode()
	browserURL := fmt.Sprintf("%s/?%s", lan, q)
	obsURL := fmt.Sprintf("%s/replay/1?%s", lan, q)
	text := fmt.Sprintf("%s\n%s\n%s", token, browserURL, obsURL)
	buttons := container.NewHBox(
		widget.NewButton(lang.Localize("Copy token"), func() { window.Clipboard().SetContent(token) }),
		widget.NewButton(lang.Localize("Copy browser or Tracker URL"), func() { window.Clipboard().SetContent(browserURL) }),
		widget.NewButton(lang.Localize("Copy OBS URL"), func() { window.Clipboard().SetContent(obsURL) }),
	)
	if cfg.PublicURL != "" {
		publicURL := fmt.Sprintf("%s/?%s", cfg.PublicURL, q)
		text += fmt.Sprintf("\n%s/\n%s", cfg.PublicURL, publicURL)
		buttons.Add(widget.NewButton(lang.Localize("Copy public sign-in URL"), func() { window.Clipboard().SetContent(publicURL) }))
	}
	values := widget.NewLabel(text)
	values.Selectable = true
	values.Wrapping = fyne.TextWrapBreak
	values.TextStyle = fyne.TextStyle{Monospace: true}
	content := container.NewVBox(widget.NewLabel(lang.Localize("The token will not be shown again.")), values, buttons)
	d := dialog.NewCustom(viewer.Name, lang.Localize("Close"), content, window)
	d.Resize(fyne.NewSize(760, 0))
	d.Show()
}
