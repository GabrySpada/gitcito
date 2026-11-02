# Manual checks

Behaviour only a person on real hardware can confirm. Each item has exact steps. Automation covers the rest
(Go tests, vitest, e2e on the server build, native smoke on Linux under Xvfb).

## All platforms
1. **Frame isolation in the native webview.** The e2e suite proves the *server build* refuses bound calls from
   `Origin: null` and foreign origins. The desktop build serves the page from Wails' own scheme instead.
   Steps: open a repository with a PDF, open the PDF preview, open the webview inspector (dev build), and in the
   PDF frame's console run `fetch('/wails/runtime',{method:'POST',body:'{}'})`. Expect a failure, not a 2xx/422.
2. **Native file drop.** Drag a file from the OS file manager onto (a) the file tree, (b) the terminal panel,
   (c) the repository chat panel. Expect: (a) the copy/replace prompt, (b) the quoted path typed into the shell,
   (c) a file chip. Drag onto any other area: nothing happens.
3. **Clipboard after an await.** Commit details → "Copy SHA". Paste elsewhere: the SHA is there.
4. **Zoom.** View → Zoom in / out / reset, then restart: the renderer's stored zoom is applied.
5. **Timelapse export.** Repository → Timelapse → Export. macOS/Linux (WebKit): an `.mp4` is offered and
   plays. Windows (WebView2): a `.webm`.

## macOS
1. **Menu bar.** Every menu renders, accelerators fire, Edit → Copy/Paste/Undo work inside text fields,
   `showOnly` items show no shortcut (Wails cannot display an accelerator without registering it).
2. **Traffic lights** sit inside the title bar inset and do not overlap the tab strip.
3. **Keychain prompt** appears only after Gitcito's own explanation dialog, on first token use.
4. **Notarization / Gatekeeper** on a signed release build: first launch opens without the "unidentified
   developer" dialog.
5. **Single instance.** With Gitcito open, run `gitcito ~/some/repo` from a terminal: the existing window comes
   to the front and opens the repository; no second window.

## Windows
1. **Frameless window.** Drag the window by the title bar area; double-click maximizes; the custom buttons work.
2. **SmartScreen** on the unsigned NSIS installer: "More info → Run anyway" is required (as in 4.x).
3. **Single instance** as on macOS.
4. **Open With picker** (`shell:openWithPicker`) shows the system "Open with" dialog.

## Linux
1. **Single instance needs a D-Bus session bus.** Under a normal desktop session it works as on macOS. Without
   one (bare X server, some containers) Gitcito logs "single instance disabled" and a second launch opens a
   second window (DECISIONS 015).
2. **PDF preview.** On a WebKitGTK build without a PDF viewer the frame is empty; the "Open in default app"
   button below it must open the system viewer.
3. **Keyring.** Without a Secret Service provider (gnome-keyring/KWallet), the keychain reports unavailable and
   tokens stay in memory for the session only.

## Release
1. **Bundle size.** Compare the macOS `.app` of 4.x (Electron) with the 5.0 Wails `.app` (Linux numbers are in
   the PR; macOS could not be built in the port environment).
