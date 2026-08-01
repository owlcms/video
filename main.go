package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"github.com/owlcms/video/internal/assets"
	"github.com/owlcms/video/internal/cameras"
	"github.com/owlcms/video/internal/config"
	"github.com/owlcms/video/internal/logging"
	"github.com/owlcms/video/internal/menubar"
	"github.com/owlcms/video/internal/replays"
	"github.com/owlcms/video/internal/videoconfig"
)

// moduleSelection is the set of modules whose tabs are visible.
type moduleSelection struct {
	cameras bool
	replays bool
}

func main() {
	configDir := flag.String("configDir", "", "directory containing cameras.toml, replays.toml, and ffmpeg.toml (default: ./"+videoconfig.DefaultRoot+")")
	extractConfig := flag.Bool("extractConfig", false, "create missing configuration files in configDir and exit")
	enableCameras := flag.Bool("cameras", false, "show the Cameras tabs at startup")
	disableCameras := flag.Bool("no-cameras", false, "hide the Cameras tabs at startup")
	enableReplays := flag.Bool("replays", false, "show the Replays tab at startup")
	disableReplays := flag.Bool("no-replays", false, "hide the Replays tab at startup")
	includeAll := flag.Bool("all", false, "include all camera sources, including raw formats (typically integrated cameras)")
	startPort := flag.Int("startport", 0, "starting port for multicast allocation (overrides cameras.toml)")
	flag.Parse()

	selection, err := resolveSelection(*enableCameras, *disableCameras, *enableReplays, *disableReplays)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	replaysAvailable := selection.replays

	paths, err := videoconfig.Resolve(*configDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	// Every legacy config.GetInstallDir() consumer must see the same directory.
	config.ConfigDir = paths.Root
	if *extractConfig {
		if err := paths.ExtractDefaults(); err != nil {
			fmt.Fprintf(os.Stderr, "extract configuration: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Created video configuration files in: %s\n", paths.Root)
		return
	}
	if err := paths.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\nRun with --extractConfig to create the required files.\n", err)
		os.Exit(1)
	}

	if err := logging.InitWithFile(filepath.Join(paths.Root, "logs"), "video.log"); err != nil {
		fmt.Fprintf(os.Stderr, "initialize logging: %v\n", err)
		os.Exit(1)
	}

	if err := cameras.Init(cameras.Options{
		CamerasConfigPath: paths.Cameras,
		FFmpegConfigPath:  paths.FFmpeg,
		IncludeAll:        *includeAll,
		StartPort:         *startPort,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	if err := replays.Init(replays.Options{
		ConfigPath:        paths.Replays,
		CamerasConfigPath: paths.Cameras,
		CamerasAvailable:  selection.cameras,
		Enabled:           replaysAvailable,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	os.Setenv("FYNE_TELEMETRY", "0")

	myApp := app.NewWithID("app.owlcms.video")
	myApp.Settings().SetTheme(cameras.Theme(theme.DefaultTheme()))
	cameras.SetAppIcon(myApp)
	window := myApp.NewWindow("OWLCMS Video")
	window.SetIcon(assets.IconResource)
	window.Resize(cameras.WindowSize())
	window.CenterOnScreen()

	camerasUI := cameras.BuildUI(window)
	replaysUI := replays.BuildUI(window)

	monitoringTab := container.NewTabItem("Monitoring", camerasUI.Monitoring)
	configurationTab := container.NewTabItem("Configuration", camerasUI.Configuration)
	replaysTab := container.NewTabItem("Replays", replaysUI.Content)
	tabs := container.NewAppTabs()

	var (
		mainMenu                 *fyne.MainMenu
		camerasItem, replaysItem *fyne.MenuItem
	)
	refreshTabs := func() {
		visible := make([]*container.TabItem, 0, 3)
		if selection.cameras {
			visible = append(visible, monitoringTab, configurationTab)
		}
		if selection.replays {
			visible = append(visible, replaysTab)
		}
		if len(visible) == 0 {
			selection.cameras = true
			visible = append(visible, monitoringTab, configurationTab)
		}
		replaysUI.SetCamerasAvailable(selection.cameras)
		camerasItem.Checked = selection.cameras
		replaysItem.Checked = selection.replays
		tabs.SetItems(visible)
		tabs.Select(visible[0])
		if mainMenu != nil {
			mainMenu.Refresh()
		}
	}

	camerasItem = fyne.NewMenuItem("Cameras", func() {
		selection.cameras = !selection.cameras
		refreshTabs()
		if selection.cameras {
			go func() {
				if err := replays.RefreshLocalCameras(); err != nil {
					logging.ErrorLogger.Printf("Failed to refresh Replays from local Cameras config: %v", err)
				}
			}()
		}
	})
	replaysItem = fyne.NewMenuItem("Replays", func() {
		selection.replays = !selection.replays
		refreshTabs()
	})

	quit := func() {
		window.SetCloseIntercept(nil)
		camerasUI.Stop()
		replays.Shutdown()
		myApp.Quit()
	}
	requestQuit := func() {
		confirmVideoShutdown(window, selection.cameras, replaysAvailable, quit)
	}

	menus := []*fyne.Menu{
		fyne.NewMenu("File",
			fyne.NewMenuItem("Open Configuration Directory", func() {
				if err := openConfigurationDirectory(paths.Root); err != nil {
					dialog.ShowError(err, window)
				}
			}),
			fyne.NewMenuItemSeparator(),
			fyne.NewMenuItem("Quit", requestQuit),
		),
		fyne.NewMenu("Modules", camerasItem, replaysItem),
	}
	menus = append(menus, camerasUI.Menus...)
	menus = append(menus, replaysUI.Menus...)
	menus = append(menus, fyne.NewMenu("Help",
		fyne.NewMenuItem("About", func() {
			dialog.ShowInformation("About",
				fmt.Sprintf("OWLCMS Video\nVersion %s", config.GetProgramVersion()), window)
		}),
	))

	mainMenu = fyne.NewMainMenu(menus...)
	refreshTabs()
	window.SetMainMenu(mainMenu)
	window.SetContent(menubar.WithDarwinMenu(mainMenu, tabs))
	window.SetCloseIntercept(requestQuit)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fyne.Do(quit)
	}()

	window.Show()
	replaysUI.StartServer(selection.cameras, replaysAvailable)
	if selection.cameras {
		camerasUI.Start()
	}
	if replaysAvailable {
		replaysUI.Start()
	}
	myApp.Run()

	cameras.Cleanup()
}

// resolveSelection turns the module flags into the initial tab selection.
// Both modules are shown unless the flags say otherwise.
func resolveSelection(enableCameras, disableCameras, enableReplays, disableReplays bool) (moduleSelection, error) {
	if enableCameras && disableCameras {
		return moduleSelection{}, fmt.Errorf("--cameras and --no-cameras are mutually exclusive")
	}
	if enableReplays && disableReplays {
		return moduleSelection{}, fmt.Errorf("--replays and --no-replays are mutually exclusive")
	}

	selection := moduleSelection{cameras: true, replays: true}
	// An explicit --cameras or --replays narrows the selection to what was asked for.
	if enableCameras || enableReplays {
		selection = moduleSelection{cameras: enableCameras, replays: enableReplays}
	}
	if disableCameras {
		selection.cameras = false
	}
	if disableReplays {
		selection.replays = false
	}
	if !selection.cameras && !selection.replays {
		return moduleSelection{}, fmt.Errorf("at least one of the Cameras or Replays modules must be enabled")
	}
	return selection, nil
}

func confirmVideoShutdown(window fyne.Window, camerasActive, replaysActive bool, proceed func()) {
	stopping := make([]string, 0, 2)
	if camerasActive {
		stopping = append(stopping, "camera streams")
	}
	if replaysActive {
		stopping = append(stopping, "jury replays and any ongoing recordings")
	}
	if len(stopping) == 0 {
		proceed()
		return
	}

	message := fmt.Sprintf("Are you sure you want to exit? This will stop %s.", joinWithAnd(stopping))
	confirmDialog := dialog.NewConfirm("Confirm Exit", message, func(confirm bool) {
		if confirm {
			logging.InfoLogger.Println("User requested application shutdown")
			proceed()
		}
	}, window)
	confirmDialog.SetDismissText("Cancel")
	confirmDialog.SetConfirmText("Exit")
	confirmDialog.Show()
}

func joinWithAnd(values []string) string {
	if len(values) < 2 {
		return values[0]
	}
	return strings.Join(values[:len(values)-1], ", ") + " and " + values[len(values)-1]
}
