# Author's note
I vibe-coded this project. The keyboard shorcut is binded through GNOME custom shortcut UI, which then is binded to my Logitech mouse's side button through `Input Remapper`. 

This is for personal use. I'm on Ubuntu, X11 (initially, `wtype` didn't work, so I switched to X11). 

Usage: Click mouse's side button, talk, click again, text appears. 

Customizable parts:
- STT provider.
- Activation and stop sound.
- Output directory for temporary `.wav` file. 
- Logic for knowing is there a recording going on.
- Mechanism to type text to where the cursor in is. System-specific. 

# Dictation CLI

Small Go CLI to transcribe a WAV dropped into the current directory and insert the text at the cursor.

What it does
- If no `*.wav` present: plays a short pip and notifies "Recording ready" so you can record into this folder.
- If a `*.wav` exists: plays a pip, uploads the newest WAV to OpenAI Whisper, copies/transmits the transcription into the active app (xdotool/clipboard), and deletes the WAV.

Requirements
- Linux (GNOME/X11 or Wayland)
- Tools: `xdotool`, `paplay` or `aplay`, `notify-send`. For Wayland: `wl-copy` (preferred) or `xclip` + `xdotool` as fallback.
- Environment: `OPENAI_API_KEY` set.

Build
```
cd /home/kyle/dictation
go build -o dictate
```

Usage
- Bind the `dictate` binary to a keyboard shortcut.
- Press once to prepare recording (hear pip + notification), save a WAV into the folder, then press again to transcribe and insert.

Notes
- On Wayland the program will copy to clipboard and notify you to paste if `xdotool` cannot simulate a paste.
