// Voiceover via the macOS `say` CLI (a host tool — invoked, never vendored, no Python). Each narrated
// line becomes an AIFF the audio mixer places on the final timeline. On non-macOS (no `say`) it returns
// null and the caller continues without narration.
//
// IMPORTANT: `say` SILENTLY substitutes the default voice when the requested one isn't found (a bad name
// exits 0). And Siri voices are NOT available to `say` — only voices listed by `say -v '?'` are. So we
// verify the requested voice exists and, if not, fall back to a voice matching the target LANGUAGE
// (never a wrong-language default), so French narration is never read by an English voice.
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

interface InstalledVoice {
  name: string;
  lang: string; // e.g. "fr_FR"
}

async function listVoices(): Promise<InstalledVoice[]> {
  try {
    const { stdout } = await pExecFile("say", ["-v", "?"], { maxBuffer: 4 * 1024 * 1024 });
    return stdout
      .split("\n")
      .map((line) => line.match(/^(.*?)\s+([a-z]{2}_[A-Z]{2})\b/))
      .filter((m): m is RegExpMatchArray => !!m)
      .map((m) => ({ name: m[1].trim(), lang: m[2] }));
  } catch {
    return [];
  }
}

// Good, natural-ish, non-novelty voices to prefer when falling back, by language.
const PREFERRED = /^(Thomas|Jacques|Am[eé]lie|Aur[eé]lie|Audrey|Marie|Samantha|Alex|Daniel|Ava|Zoe|Serena|Allison|Nicky)/i;

function resolveVoice(
  installed: InstalledVoice[],
  requested: string | undefined,
  lang: string,
): string | undefined {
  if (requested && installed.some((v) => v.name === requested)) return requested;
  const byLang = installed.filter((v) => v.lang.startsWith(lang));
  const good = byLang.find((v) => PREFERRED.test(v.name)) ?? byLang[0];
  if (requested) {
    console.warn(
      `! voice "${requested}" is not available to \`say\` (Siri voices aren't) — ` +
        `using "${good?.name ?? "system default"}" for language "${lang}".`,
    );
  }
  return good?.name;
}

// Synthesize one AIFF per (text, offset) with a validated voice + optional speaking rate (wpm).
export async function synthVoiceovers(
  lines: { text: string; atSec: number }[],
  workDir: string,
  opts: { voice?: string; rate?: number; lang?: string } = {},
): Promise<VoiceClip[]> {
  if (lines.length === 0) return [];
  const installed = await listVoices();
  if (installed.length === 0) return []; // no `say`
  const voice = resolveVoice(installed, opts.voice, opts.lang ?? "en");

  const out: VoiceClip[] = [];
  for (let i = 0; i < lines.length; i++) {
    const file = path.join(workDir, `vo_${i}.aiff`);
    const args = ["-o", file];
    if (voice) args.push("-v", voice);
    if (opts.rate) args.push("-r", String(opts.rate));
    await pExecFile("say", [...args, lines[i].text]).catch(() => {});
    const durSec = await probeDuration(file).catch(() => 0);
    if (durSec > 0) out.push({ file, atSec: lines[i].atSec, durSec });
  }
  return out;
}
