# LANdrop

AirDrop with chat, for any LAN, on macOS and Windows. One small native binary, no server, no accounts.

- **Discover** every other computer on the LAN running LANdrop (UDP multicast).
- **Chat** with any of them (direct TCP).
- **Send files and folders** as fast as the network allows: raw TCP stream, resumable, with a progress bar and cancel button per transfer. Drag and drop onto the window or use the file dialog. Received files land in `~/Downloads`.

## Use

Start `landrop` on two or more machines on the same network. Select a computer on the left, then type a message and press Enter, or drop files or folders on the window. Type `/name Alice` to change how you appear to others (the hostname is the default; the name is remembered).

## Build

Requires Go and a C compiler (Xcode command line tools on macOS, MSYS2/MinGW on Windows).

```sh
go build -o landrop .
go test ./...
```

Prebuilt binaries: [Releases](../../releases). `latest` tracks the main branch. The macOS app is unsigned: right-click, Open on first launch, or `xattr -dr com.apple.quarantine LANdrop.app`.

## How it works

Each instance announces itself on `239.77.77.77:7777` every two seconds and listens on a random TCP port. A transfer is one TCP connection: a JSON header line, then the receiver replies with the byte offset it already has in its `.part` file, then the sender streams the rest. No encryption: use it only on networks you trust.
