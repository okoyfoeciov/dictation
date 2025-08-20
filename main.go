package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"math"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		fatal(err)
	}

	wavs, err := filepath.Glob(filepath.Join(cwd, "*.wav"))
	if err != nil {
		fatal(err)
	}
	// If no wav exists, start recording into a fixed file and write pidfile
	const recordFile = "dictation_recording.wav"
	const pidFile = ".dictation_recording.pid"

	if len(wavs) == 0 {
		// Start-recording action
		if err := startRecording(recordFile, pidFile); err != nil {
			notify("Dictation", "Could not start recorder: "+err.Error())
			fatal(err)
		}
		// play "on" sound when recording starts
		playPip(true)
		return
	}

	// There is at least one wav. If pidfile exists, stop the recorder first.
	if _, err := os.Stat(pidFile); err == nil {
		if err := stopRecording(pidFile); err != nil {
			notify("Dictation", "Could not stop recorder: "+err.Error())
			fatal(err)
		}
		// small pause to ensure the WAV is flushed to disk
		time.Sleep(300 * time.Millisecond)
	}

	// Stop/transcribe action: pick newest wav
	sort.Slice(wavs, func(i, j int) bool {
		iInfo, _ := os.Stat(wavs[i])
		jInfo, _ := os.Stat(wavs[j])
		return iInfo.ModTime().After(jInfo.ModTime())
	})

	wav := wavs[0]
	// play "off" sound when recording stops / before transcribing
	playPip(false)

	text, err := transcribe(wav)
	if err != nil {
		notify("Dictation", "Transcription failed: "+err.Error())
		fatal(err)
	}

	// Insert text at cursor
	if err := typeText(text); err != nil {
		notify("Dictation", "Insert failed: "+err.Error())
		fatal(err)
	}

	// delete processed file so next invocation sees no wav
	if err := os.Remove(wav); err != nil {
		// deletion is non-fatal; log to stderr only
		fmt.Fprintln(os.Stderr, "warning: could not delete wav:", err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func notify(title, body string) {
	_ = exec.Command("notify-send", title, body).Run()
}

func playPip(on bool) {
	// prefer playing packaged mp3 files if present: on.mp3 / off.mp3
	var target string
	if on {
		target = "on.mp3"
	} else {
		target = "off.mp3"
	}

	// if file exists, try to play it with common players
	if _, err := os.Stat(target); err == nil {
		// try paplay (PulseAudio) which can play many formats
		players := [][]string{
			// {"aplay", target},
			{"ffplay", "-nodisp", "-autoexit", target},
		}
		for _, p := range players {
			if !pathExists(p[0]) {
				continue
			}
			cmd := exec.Command(p[0], p[1:]...)
			// silence output
			cmd.Stdout = nil
			cmd.Stderr = nil
			if err := cmd.Run(); err == nil {
				return
			}
		}
	}

	// Fallback: generate a short 220Hz sine WAV in memory and try to play with paplay/aplay
	b, err := generateSineWav(220, 0.09)
	if err != nil {
		return
	}

	// try paplay
	cmd := exec.Command("paplay")
	cmd.Stdin = bytes.NewReader(b)
	if err := cmd.Run(); err == nil {
		return
	}
	// try aplay
	cmd = exec.Command("aplay")
	cmd.Stdin = bytes.NewReader(b)
	if err := cmd.Run(); err == nil {
		return
	}
	// fallback: bell
	fmt.Print("\a")
}

func generateSineWav(freqHz float64, seconds float64) ([]byte, error) {
	// 16kHz, 16-bit PCM mono
	sampleRate := 16000
	nSamples := int(float64(sampleRate) * seconds)
	buf := &bytes.Buffer{}
	// RIFF header placeholder
	buf.WriteString("RIFF")
	binary.Write(buf, binary.LittleEndian, uint32(0)) // placeholder for chunk size
	buf.WriteString("WAVE")
	// fmt subchunk
	buf.WriteString("fmt ")
	binary.Write(buf, binary.LittleEndian, uint32(16)) // subchunk1 size
	binary.Write(buf, binary.LittleEndian, uint16(1))  // PCM
	binary.Write(buf, binary.LittleEndian, uint16(1))  // channels
	binary.Write(buf, binary.LittleEndian, uint32(sampleRate))
	byteRate := uint32(sampleRate * 2)
	binary.Write(buf, binary.LittleEndian, byteRate)
	blockAlign := uint16(2)
	binary.Write(buf, binary.LittleEndian, blockAlign)
	binary.Write(buf, binary.LittleEndian, uint16(16)) // bits per sample
	// data subchunk
	buf.WriteString("data")
	binary.Write(buf, binary.LittleEndian, uint32(nSamples*2))

	for i := 0; i < nSamples; i++ {
		t := float64(i) / float64(sampleRate)
		sample := int16(math.Round(32767 * 0.3 * math.Sin(2*math.Pi*freqHz*t)))
		binary.Write(buf, binary.LittleEndian, sample)
	}

	// fill in chunk size
	b := buf.Bytes()
	chunkSize := uint32(len(b) - 8)
	binary.LittleEndian.PutUint32(b[4:8], chunkSize)
	return b, nil
}

func transcribe(wavPath string) (string, error) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return "", errors.New("OPENAI_API_KEY not set")
	}

	f, err := os.Open(wavPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	fw, err := w.CreateFormFile("file", filepath.Base(wavPath))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(fw, f); err != nil {
		return "", err
	}
	_ = w.WriteField("model", "whisper-1")
	w.Close()

	req, err := http.NewRequest("POST", "https://api.openai.com/v1/audio/transcriptions", &b)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+apiKey)

	cli := &http.Client{Timeout: 120 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := ioutil.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("openai error: %s", string(body))
	}

	var js struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(body, &js); err != nil {
		return "", err
	}
	return js.Text, nil
}

func typeText(text string) error {
	// If Wayland is in use, prefer copying to the clipboard (wl-copy) and
	// asking the user to paste. If wl-copy isn't available but xclip and
	// xdotool are, try copying with xclip and simulate a Ctrl+V paste.
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		// Prefer typing directly with xdotool when available.
		if pathExists("xdotool") {
			cmd := exec.Command("xdotool", "type", "--clearmodifiers", text)
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err == nil {
				return nil
			}
			// if typing fails, fall through to wl-copy fallback
		}

		// Fallback: copy to Wayland clipboard with wl-copy and notify the user to paste.
		if pathExists("wl-copy") {
			cmd := exec.Command("wl-copy")
			cmd.Stdin = strings.NewReader(text)
			if err := cmd.Run(); err == nil {
				notify("Dictation", "Transcribed text copied to clipboard — please paste into target app")
				return nil
			}
		}

		return errors.New("no Wayland typing tools found; install wl-clipboard (wl-copy) or xdotool")
	}

	// X11 session: prefer typing with xdotool, else use clipboard + simulated paste.
	// Ensure DISPLAY is present (basic sanity check for X11).
	if os.Getenv("DISPLAY") == "" {
		return errors.New("no X11 DISPLAY found; run under an X11 session or set DISPLAY")
	}

	// 1) Try direct typing with xdotool
	if pathExists("xdotool") {
		cmd := exec.Command("xdotool", "type", "--clearmodifiers", text)
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err == nil {
			return nil
		}
		// fallthrough to clipboard-based approaches
	}

	// 2) Try copying to clipboard with xclip or xsel, then simulate paste with xdotool
	if pathExists("xclip") || pathExists("xsel") {
		var clipCmd *exec.Cmd
		if pathExists("xclip") {
			clipCmd = exec.Command("xclip", "-selection", "clipboard")
		} else {
			clipCmd = exec.Command("xsel", "--clipboard", "--input")
		}
		clipCmd.Stdin = strings.NewReader(text)
		if err := clipCmd.Run(); err == nil {
			if pathExists("xdotool") {
				// Simulate Ctrl+V to paste from clipboard
				cmd := exec.Command("xdotool", "key", "--clearmodifiers", "ctrl+v")
				cmd.Stderr = os.Stderr
				if err := cmd.Run(); err == nil {
					return nil
				}
			}
			// If we can't simulate paste, notify user that clipboard contains text
			notify("Dictation", "Transcribed text copied to clipboard — please paste into target app")
			return nil
		}
	}

	// 3) Last resort: try wl-copy (might work under XWayland setups) or tell user how to proceed
	if pathExists("wl-copy") {
		cmd := exec.Command("wl-copy")
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err == nil {
			notify("Dictation", "Transcribed text copied to clipboard — please paste into target app")
			return nil
		}
	}

	return errors.New("no X11 typing tools found; install xdotool, xclip (or xsel), or wl-clipboard")
}

func moveProcessed(path string) error {
	procDir := "processed"
	if err := os.MkdirAll(procDir, 0755); err != nil {
		return err
	}
	base := filepath.Base(path)
	dst := filepath.Join(procDir, fmt.Sprintf("%d_%s", time.Now().Unix(), base))
	return os.Rename(path, dst)
}

func startRecording(outFile, pidFile string) error {
	// Use arecord to capture 16kHz mono 16-bit WAV
	// arecord -f S16_LE -r 16000 -c 1 out.wav
	cmd := exec.Command("arecord", "-f", "S16_LE", "-r", "16000", "-c", "1", outFile)
	if err := cmd.Start(); err != nil {
		return err
	}
	// write pid
	pid := cmd.Process.Pid
	if err := ioutil.WriteFile(pidFile, []byte(strconv.Itoa(pid)), 0644); err != nil {
		// try to kill process if we couldn't write pid
		_ = cmd.Process.Kill()
		return err
	}
	// detach: do not wait here
	go func() {
		_ = cmd.Wait()
		// cleanup pidfile when process exits
		_ = os.Remove(pidFile)
	}()
	return nil
}

func stopRecording(pidFile string) error {
	b, err := ioutil.ReadFile(pidFile)
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(string(bytes.TrimSpace(b)))
	if err != nil {
		return err
	}
	// send SIGINT to allow arecord to flush
	if err := syscall.Kill(pid, syscall.SIGINT); err != nil {
		// fallback: SIGKILL
		if killErr := syscall.Kill(pid, syscall.SIGKILL); killErr != nil {
			return fmt.Errorf("kill failed: %v (also tried SIGKILL: %v)", err, killErr)
		}
	}
	// remove pidfile
	_ = os.Remove(pidFile)
	return nil
}

func pathExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
