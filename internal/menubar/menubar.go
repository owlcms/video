// Package menubar provides the macOS in-window representation of a Fyne main menu.
package menubar

import (
	"image/color"
	"runtime"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

var _ desktop.Hoverable = (*menuButton)(nil)

// WithDarwinMenu adds an in-window representation of mainMenu above content
// on macOS, where the native main menu is outside the application window.
func WithDarwinMenu(mainMenu *fyne.MainMenu, content fyne.CanvasObject) fyne.CanvasObject {
	if runtime.GOOS != "darwin" || mainMenu == nil {
		return content
	}

	buttons := make([]fyne.CanvasObject, 0, len(mainMenu.Items))
	for _, menu := range mainMenu.Items {
		buttons = append(buttons, newMenuButton(menu.Label, menu.Items))
	}

	return container.NewBorder(
		container.NewVBox(container.NewHBox(buttons...), widget.NewSeparator()),
		nil,
		nil,
		nil,
		content,
	)
}

type menuButton struct {
	widget.BaseWidget

	background *canvas.Rectangle
	hovered    bool
	label      string
	menuItems  []*fyne.MenuItem
	menuOpen   bool
}

type compactMenuTheme struct {
	fyne.Theme
}

func (t compactMenuTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	if name == theme.ColorNameOverlayBackground {
		return t.Theme.Color(theme.ColorNameMenuBackground, variant)
	}
	if name == theme.ColorNameShadow {
		return color.Transparent
	}
	return t.Theme.Color(name, variant)
}

func (t compactMenuTheme) Size(name fyne.ThemeSizeName) float32 {
	if name == theme.SizeNameInnerPadding {
		return 3
	}
	return t.Theme.Size(name)
}

func newMenuButton(label string, menuItems []*fyne.MenuItem) *menuButton {
	button := &menuButton{label: label, menuItems: menuItems}
	button.ExtendBaseWidget(button)
	return button
}

func (b *menuButton) AccessibilityLabel() string {
	return b.label
}

func (b *menuButton) AccessibilityRole() fyne.AccessibleRole {
	return fyne.AccessibleRoleButton
}

func (b *menuButton) CreateRenderer() fyne.WidgetRenderer {
	b.background = canvas.NewRectangle(color.Transparent)
	b.updateBackground()
	return widget.NewSimpleRenderer(container.NewStack(b.background, b.menuLabel()))
}

func (b *menuButton) MinSize() fyne.Size {
	return b.menuLabel().MinSize().Add(fyne.NewSize(12, 4))
}

func (b *menuButton) MouseIn(*desktop.MouseEvent) {
	b.hovered = true
	b.updateBackground()
}

func (b *menuButton) MouseMoved(*desktop.MouseEvent) {
}

func (b *menuButton) MouseOut() {
	b.hovered = false
	b.updateBackground()
}

func (b *menuButton) Tapped(*fyne.PointEvent) {
	b.menuOpen = true
	b.updateBackground()
	showCompactMenu(b)
}

func (b *menuButton) menuLabel() *canvas.Text {
	label := canvas.NewText(b.label, b.Theme().Color(theme.ColorNameForeground, fyne.CurrentApp().Settings().ThemeVariant()))
	label.Alignment = fyne.TextAlignCenter
	label.TextSize = theme.TextSize()
	return label
}

func (b *menuButton) updateBackground() {
	if b.background == nil {
		return
	}

	backgroundColor := color.Color(color.Transparent)
	if b.hovered || b.menuOpen {
		backgroundColor = b.Theme().Color(theme.ColorNameHover, fyne.CurrentApp().Settings().ThemeVariant())
	}
	b.background.FillColor = backgroundColor
	b.background.Refresh()
}

func showCompactMenu(button *menuButton) {
	app := fyne.CurrentApp()
	if app == nil || app.Driver() == nil {
		button.menuOpen = false
		button.updateBackground()
		return
	}

	driver := app.Driver()
	menuCanvas := driver.CanvasForObject(button)
	if menuCanvas == nil {
		button.menuOpen = false
		button.updateBackground()
		return
	}

	compactTheme := compactMenuTheme{Theme: button.Theme()}
	variant := fyne.CurrentApp().Settings().ThemeVariant()
	menuBackground := compactTheme.Color(theme.ColorNameMenuBackground, variant)
	menu := widget.NewMenu(fyne.NewMenu("", button.menuItems...))
	topInset := canvas.NewRectangle(menuBackground)
	topInset.SetMinSize(fyne.NewSize(1, 3))
	leftInset := canvas.NewRectangle(menuBackground)
	leftInset.SetMinSize(fyne.NewSize(3, 1))
	rightInset := canvas.NewRectangle(menuBackground)
	rightInset.SetMinSize(fyne.NewSize(3, 1))
	content := container.NewBorder(
		topInset,
		nil,
		leftInset,
		rightInset,
		container.NewThemeOverride(menu, compactTheme),
	)
	popup := widget.NewPopUp(container.NewThemeOverride(content, compactTheme), menuCanvas)
	container.NewThemeOverride(popup, compactTheme)
	menu.OnDismiss = func() {
		popup.Hide()
		button.menuOpen = false
		button.updateBackground()
	}

	position := driver.AbsolutePositionForObject(button)
	position.Y += button.Size().Height
	popup.ShowAtPosition(position)
}
