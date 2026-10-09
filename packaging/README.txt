TunnelTab {{VERSION}}
=====================

Portable SSH terminal and web-UI launcher for your own servers.
No install, no accounts. Everything stays in this folder.

FIRST: EXTRACT THE ZIP
  Don't run tunneltab.exe from inside the ZIP file. Right-click the ZIP,
  choose "Extract All...", and use the extracted "tunneltab" folder.

START
  Windows:  double-click tunneltab.exe
  Linux:    ./tunneltab-linux-amd64

  Your dashboard opens in your web browser. The first time, you'll
  create a master password. Keep it safe: it cannot be recovered.

WINDOWS SMARTSCREEN
  The program isn't code-signed, so Windows may show "Windows protected
  your PC". Click "More info", then "Run anyway".

ANTIVIRUS WARNINGS
  Windows Security or another antivirus program may flag or
  quarantine tunneltab.exe (for example as Trojan:Win32/Wacatac.B!ml).
  Unsigned programs often trigger such false alarms. Check your
  download against SHA256SUMS.txt on the release page. To allow it
  in Windows Security: Virus & threat protection, Protection
  history, the entry, Actions, "Allow on device".

YOUR DATA
  Everything you save is in the "data" folder next to the program,
  encrypted with your master password. To move TunnelTab to another
  PC or a USB stick, quit it and copy this whole folder.

STOPPING
  Use the Quit button in the dashboard, or Quit TunnelTab in the tray
  icon's menu. Closing the browser tab also quits TunnelTab, after a
  few seconds. To keep it running in the tray instead, choose "Keep
  TunnelTab running" in Settings, General.

FILES
  tunneltab.exe             the Windows program
  tunneltab-linux-amd64     the Linux program
  LICENSE.txt               TunnelTab's license (MIT)
  THIRD_PARTY_NOTICES.txt   licenses of included open-source software
  data\                     created on first run

User guide, troubleshooting and FAQ:
  https://github.com/Aerobit/TunnelTab/blob/main/docs/USER_GUIDE.md
