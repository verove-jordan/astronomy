// Thin wrappers around host ffmpeg/ffprobe (the same binaries the engine uses; override via FFMPEG_BIN
// / FFPROBE_BIN). No shell — args are passed as an array so filtergraphs never need quoting.
import { execFile } from "node:child_process";
import { promisify } from "node:util";

const pExecFile = promisify(execFile);

export const FFMPEG = process.env.FFMPEG_BIN || "ffmpeg";
export const FFPROBE = process.env.FFPROBE_BIN || "ffprobe";

// Shared H.264 encode settings — identical across every clip so the concat demuxer can stream-copy.
export const ENCODE = [
  "-c:v", "libx264",
  "-preset", "medium",
  "-crf", "18",
  "-pix_fmt", "yuv420p",
];

export async function ffmpeg(args: string[]): Promise<void> {
  try {
    await pExecFile(FFMPEG, ["-hide_banner", "-loglevel", "error", ...args], {
      maxBuffer: 64 * 1024 * 1024,
    });
  } catch (err) {
    const e = err as { stderr?: string; message?: string };
    throw new Error(`ffmpeg failed:\n${e.stderr || e.message}`);
  }
}

export async function probeDuration(file: string): Promise<number> {
  const { stdout } = await pExecFile(FFPROBE, [
    "-v", "error",
    "-show_entries", "format=duration",
    "-of", "default=nokey=1:noprint_wrappers=1",
    file,
  ]);
  const d = parseFloat(stdout.trim());
  if (!isFinite(d)) throw new Error(`could not probe duration of ${file}`);
  return d;
}

export async function hasFfmpeg(): Promise<boolean> {
  try {
    await pExecFile(FFMPEG, ["-version"]);
    return true;
  } catch {
    return false;
  }
}
