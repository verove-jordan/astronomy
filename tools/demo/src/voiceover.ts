// Voiceover via the macOS `say` CLI (a host tool — invoked, never vendored, no Python). Each narrated
// line becomes an AIFF the audio mixer places on the final timeline. On non-macOS (no `say`) it returns
// null and the caller continues without narration.
import { execFile } from "node:child_process";
import path from "node:path";
import { promisify } from "node:util";
import { probeDuration } from "./ffmpeg.js";

const pExecFile = promisify(execFile);

export interface VoiceClip {
  file: string;
  atSec: number; // start offset on the final timeline
  durSec: number;
}

async function hasSay(): Promise<boolean> {
  if (process.platform !== "darwin") return false;
  try {
    await pExecFile("say", ["-v", "?"]);
    return true;
  } catch {
    return false;
  }
}

// Synthesize one AIFF per (text, offset). Returns [] when `say` is unavailable so the demo still renders.
export async function synthVoiceovers(
  lines: { text: string; atSec: number }[],
  workDir: string,
): Promise<VoiceClip[]> {
  if (lines.length === 0 || !(await hasSay())) return [];
  const out: VoiceClip[] = [];
  for (let i = 0; i < lines.length; i++) {
    const file = path.join(workDir, `vo_${i}.aiff`);
    await pExecFile("say", ["-o", file, lines[i].text]);
    const durSec = await probeDuration(file).catch(() => 0);
    out.push({ file, atSec: lines[i].atSec, durSec });
  }
  return out;
}
