// Build the final audio track: a silent base of the exact video length, optional looped background music
// (ducked low), and each voiceover clip delayed to its place on the timeline — mixed to one AAC file.
// Returns null when there is nothing to add (no music, no narration), so the caller skips the audio mux.
import path from "node:path";
import { ffmpeg } from "./ffmpeg.js";
import type { VoiceClip } from "./voiceover.js";

export async function buildAudio(opts: {
  workDir: string;
  totalSec: number;
  music: string | null;
  voices: VoiceClip[];
}): Promise<string | null> {
  const { workDir, totalSec, music, voices } = opts;
  if (!music && voices.length === 0) return null;

  const inputs: string[] = [
    "-f", "lavfi", "-t", totalSec.toFixed(3), "-i", "anullsrc=r=44100:cl=stereo",
  ];
  const parts: string[] = ["[0:a]aresample=44100,volume=1[base]"];
  const mixLabels: string[] = ["[base]"];

  let idx = 1;
  if (music) {
    inputs.push("-stream_loop", "-1", "-i", music);
    parts.push(`[${idx}:a]aresample=44100,volume=0.16[mus]`);
    mixLabels.push("[mus]");
    idx++;
  }
  voices.forEach((v, k) => {
    inputs.push("-i", v.file);
    const ms = Math.max(0, Math.round(v.atSec * 1000));
    parts.push(`[${idx}:a]aresample=44100,adelay=${ms}|${ms},volume=1.1[v${k}]`);
    mixLabels.push(`[v${k}]`);
    idx++;
  });

  const filter =
    parts.join(";") +
    `;${mixLabels.join("")}amix=inputs=${mixLabels.length}:normalize=0:duration=first[a]`;

  const out = path.join(workDir, "audio.m4a");
  await ffmpeg([
    "-y",
    ...inputs,
    "-filter_complex", filter,
    "-map", "[a]",
    "-t", totalSec.toFixed(3),
    "-c:a", "aac",
    "-b:a", "192k",
    out,
  ]);
  return out;
}
