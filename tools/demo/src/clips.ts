// Encode the individual clips that make up the final video: one per timeline span (with ×N speed and/or
// Ken-Burns zoom), plus the intro/outro cards, then concatenate them. Every clip is encoded with the
// same geometry + ENCODE settings so the concat demuxer can stream-copy.
import { writeFile } from "node:fs/promises";
import path from "node:path";
import { ENCODE, ffmpeg } from "./ffmpeg.js";
import type { Span } from "./recorder.js";

export interface Geometry {
  w: number;
  h: number;
  fps: number;
}

const sec = (ms: number) => (ms / 1000).toFixed(3);

// Build the -vf chain for a body span: zero the PTS base (and divide for ×N speed), optional zoompan
// push-in, then normalise to exact geometry. Speed spans and zoom spans are disjoint in practice (the
// job wait has no zoom), but both are handled.
function spanFilters(span: Span, g: Geometry): string {
  const speed = span.speed && span.speed > 1 ? span.speed : 1;
  const filters = [`setpts=(PTS-STARTPTS)/${speed}`];
  if (span.zoomScale && span.zoomScale > 1) {
    const inMs = span.endMs - span.startMs;
    const outFrames = Math.max(1, Math.round((inMs / 1000 / speed) * g.fps));
    const inc = ((span.zoomScale - 1) / outFrames).toFixed(6);
    const cx = (span.zoomCx ?? 0.5).toFixed(4);
    const cy = (span.zoomCy ?? 0.5).toFixed(4);
    // Use `pzoom` (the PREVIOUS frame's zoom) as the accumulator — `zoom` refers to the current frame
    // being computed and does not accumulate, so the push-in never happens. The z-expression is
    // single-quoted, so its comma must NOT be backslash-escaped (a literal "\," breaks the expression).
    filters.push(
      `zoompan=z='min(pzoom+${inc},${span.zoomScale})':d=1:` +
        `x='iw*${cx}-(iw/zoom/2)':y='ih*${cy}-(ih/zoom/2)':fps=${g.fps}:s=${g.w}x${g.h}`,
    );
  }
  filters.push(`scale=${g.w}:${g.h}:flags=lanczos`, "setsar=1", "format=yuv420p");
  return filters.join(",");
}

export async function encodeSpan(
  raw: string,
  span: Span,
  idx: number,
  g: Geometry,
  workDir: string,
): Promise<string> {
  const out = path.join(workDir, `clip_${String(idx).padStart(3, "0")}.mp4`);
  await ffmpeg([
    "-y",
    "-i", raw,
    "-ss", sec(span.startMs),
    "-to", sec(span.endMs),
    "-an",
    "-vf", spanFilters(span, g),
    "-r", String(g.fps),
    ...ENCODE,
    out,
  ]);
  return out;
}

export async function encodeCard(
  png: string,
  seconds: number,
  g: Geometry,
  out: string,
): Promise<string> {
  const fadeOut = Math.max(0, seconds - 0.4).toFixed(2);
  await ffmpeg([
    "-y",
    "-loop", "1",
    "-t", seconds.toFixed(2),
    "-i", png,
    "-vf",
    `scale=${g.w}:${g.h},setsar=1,format=yuv420p,` +
      `fade=t=in:st=0:d=0.4,fade=t=out:st=${fadeOut}:d=0.4`,
    "-r", String(g.fps),
    ...ENCODE,
    out,
  ]);
  return out;
}

// Concatenate clips (already identically encoded) by stream-copy.
export async function concat(clips: string[], workDir: string): Promise<string> {
  const list = path.join(workDir, "concat.txt");
  await writeFile(
    list,
    clips.map((c) => `file '${c.replace(/'/g, "'\\''")}'`).join("\n"),
    "utf8",
  );
  const out = path.join(workDir, "combined.mp4");
  await ffmpeg(["-y", "-f", "concat", "-safe", "0", "-i", list, "-c", "copy", out]);
  return out;
}
