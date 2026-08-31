# QuickDrop

Send photos and files from your phone to your computer, wirelessly, with no cables,
no extra apps, no accounts, and no internet. It works entirely on your Wi-Fi
network — nothing is uploaded to the cloud.

## How to install

1. Download `QuickDrop Setup.exe` (see RELEASING.md for how the installer is built).
2. Run it and click **Yes**.
3. The installer checks for Microsoft's free **WebView2** component and installs it if
   needed. That is the only reason QuickDrop ships as an installer instead of a single
   plain executable.
4. Open **QuickDrop** from your Start menu.

That's it. There is nothing else to install on your phone.

## How to use

1. Open QuickDrop on the computer. The window shows a QR code within about two
   seconds, plus a backup URL.
2. Scan the code with your phone's camera (no app needed — it opens the phone's
   browser).
3. Pick photos or files. They arrive on the computer with a live progress bar.
4. Finished files are in **Downloads → QuickDrop** on the computer, shown in the app.

If a transfer is interrupted — your phone locks, you background the browser, the
network drops — QuickDrop picks back up automatically and finishes the file. It never
saves a half-finished file under its real name. A file only appears when it has arrived
100% complete and its checksum matched.

## Honest limitations

- **Same Wi-Fi required.** Phone and computer must be on the same home Wi-Fi network.
  QuickDrop usually will **not** work on hotel, office, public, or guest Wi-Fi.
- **Close the app and a transfer is lost.** If QuickDrop closes while a transfer is in
  progress, that transfer has to be started again from the phone.
- **The QR code is the door.** Anyone who can scan the QR code while a session is
  shown can send files to this computer. Only show the window when you're using it,
  and close the session when you're done (the app makes this one click).
- **One way.** v1 sends files from the phone to the computer only.
- QuickDrop is a home tool. It uses no passwords and no encryption, because it speaks
  only to devices on your own network.