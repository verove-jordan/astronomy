// Assemble the final MP4 from a recording: re-time each span (×N speed / zoom), bookend with the
// intro/outro cards, and lay down music + voiceover. See clips.ts (video) and audio.ts (sound).
import { mkdir } from "node:fs/promises";
import path from "node:path";
import { buildAudio } from "./audio.js";
import { concat, encodeCard, encodeSpan, type Geometry } from "./clips.js";
import { ffmpeg, probeDuration } from "./ffmpeg.js";
import type { RecordResult, Span } from "./recorder.js";
import { synthVoiceovers } from "./voiceover.js";

// Make the spans cover [0, durMs] exactly, contiguous, with no sub-60ms slivers (merged into the prior).
function normalizeSpans(spans: Span[], durMs: number): Span[] {
  if (spans.length === 0) return [{ name: "all", startMs: 0, endMs: durMs, speed: 1 }];
  const s = spans.map((x) => ({ ...x }));
  s[0].startMs = 0;
  for (let i = 1; i < s.length; i++) s[i].startMs = s[i - 1].endMs;
  s[s.length - 1].endMs = durMs;
  const out: Span[] = [];
  for (const span of s) {
    if (span.endMs - span.startMs < 60 && out.length) out[out.length - 1].endMs = span.endMs;
    else out.push(span);
  }
  return out;
}

const spanOutSec = (span: Span) => (span.endMs - span.startMs) / 1000 / (span.speed > 1 ? span.speed : 1);

export async function postprocess(
  rec: RecordResult,
  opts: { outFile: string; music: string | null },
): Promise<string> {
  const { meta, intro, outro } = rec.scenario;
  const g: Geometry = { w: meta.viewport[0], h: meta.viewport[1], fps: meta.fps };
  const durMs = (await probeDuration(rec.rawVideo)) * 1000;
  const spans = normalizeSpans(rec.spans, durMs);

  // 1. Body clips (one per span), plus voiceover placements on the final (post-speed) timeline.
  const bodyClips: string[] = [];
  const lines: { text: string; atSec: number }[] = [];
  let cursor = intro?.seconds ?? 0;
  for (let i = 0; i < spans.length; i++) {
    bodyClips.push(await encodeSpan(rec.rawVideo, spans[i], i, g, rec.workDir));
    if (spans[i].narrate) lines.push({ text: spans[i].narrate!, atSec: cursor });
    cursor += spanOutSec(spans[i]);
  }

  // 2. Intro/outro cards.
  const ordered: string[] = [];
  if (intro && rec.introPng)
    ordered.push(await encodeCard(rec.introPng, intro.seconds, g, path.join(rec.workDir, "intro.mp4")));
  ordered.push(...bodyClips);
  if (outro && rec.outroPng)
    ordered.push(await encodeCard(rec.outroPng, outro.seconds, g, path.join(rec.workDir, "outro.mp4")));

  // 3. Concatenate, then build + mux audio against the real combined duration.
  const combined = await concat(ordered, rec.workDir);
  const totalSec = await probeDuration(combined);
  const voices = await synthVoiceovers(lines, rec.workDir, {
    voice: meta.voice,
    rate: meta.voiceRate,
    lang: meta.lang,
  });
  // Serialize narration so lines never overlap: each starts no earlier than the previous one ends (plus
  // a short breath). Keeps the audio smooth even when a line runs longer than its step.
  const GAP = 0.35;
  voices.sort((a, b) => a.atSec - b.atSec);
  for (let i = 1; i < voices.length; i++) {
    const prevEnd = voices[i - 1].atSec + voices[i - 1].durSec;
    if (voices[i].atSec < prevEnd + GAP) voices[i].atSec = prevEnd + GAP;
  }
  const lastEnd = voices.length ? voices[voices.length - 1].atSec + voices[voices.length - 1].durSec : 0;
  if (lastEnd > totalSec + 0.5) {
    console.warn(`! narration (${lastEnd.toFixed(1)}s) exceeds the video (${totalSec.toFixed(1)}s) — trim text or add dwell.`);
  }
  const audio = await buildAudio({ workDir: rec.workDir, totalSec, music: opts.music, voices });

  await mkdir(path.dirname(opts.outFile), { recursive: true });
  if (audio) {
    await ffmpeg([
      "-y",
      "-i", combined,
      "-i", audio,
      "-map", "0:v", "-map", "1:a",
      "-c:v", "copy", "-c:a", "aac",
      "-shortest",
      opts.outFile,
    ]);
  } else {
    await ffmpeg(["-y", "-i", combined, "-c", "copy", opts.outFile]);
  }
  return opts.outFile;
}
