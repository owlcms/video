#!/bin/bash

# Must be run with sudo, not via sudo bash
if [ "$EUID" -ne 0 ]; then
    echo "Please run this script with: sudo ./setup-nosleep.sh"
    exit 1
fi

if [ -z "$SUDO_USER" ]; then
    echo "Do not run this script with 'sudo bash'. Use: sudo ./setup-nosleep.sh"
    exit 1
fi

USER_NAME="$SUDO_USER"
LOGIND_CONF="/etc/systemd/logind.conf"

echo "Applying no-sleep, no-blank, ignore-lid settings for user: $USER_NAME"

#############################################
# 1. Disable all suspend/hibernate actions
#############################################

systemctl mask sleep.target suspend.target hibernate.target hybrid-sleep.target

#############################################
# 2. Configure logind to ignore lid events
#############################################

# Replace existing lines or add them if missing
grep -q "^HandleLidSwitch=" "$LOGIND_CONF" \
    && sed -i 's/^HandleLidSwitch=.*/HandleLidSwitch=ignore/' "$LOGIND_CONF" \
    || echo "HandleLidSwitch=ignore" >> "$LOGIND_CONF"

grep -q "^HandleLidSwitchDocked=" "$LOGIND_CONF" \
    && sed -i 's/^HandleLidSwitchDocked=.*/HandleLidSwitchDocked=ignore/' "$LOGIND_CONF" \
    || echo "HandleLidSwitchDocked=ignore" >> "$LOGIND_CONF"

grep -q "^HandleLidSwitchExternalPower=" "$LOGIND_CONF" \
    && sed -i 's/^HandleLidSwitchExternalPower=.*/HandleLidSwitchExternalPower=ignore/' "$LOGIND_CONF" \
    || echo "HandleLidSwitchExternalPower=ignore" >> "$LOGIND_CONF"

#############################################
# 3. GNOME settings for the invoking user
#############################################

sudo -u "$USER_NAME" gsettings set org.gnome.settings-daemon.plugins.power sleep-inactive-ac-type 'nothing'
sudo -u "$USER_NAME" gsettings set org.gnome.desktop.session idle-delay 0
sudo -u "$USER_NAME" gsettings set org.gnome.desktop.screensaver lock-enabled false
sudo -u "$USER_NAME" gsettings set org.gnome.desktop.screensaver idle-activation-enabled false

#############################################
# 4. Restart logind last
#############################################

systemctl restart systemd-logind

echo "Done. A reboot is recommended."
