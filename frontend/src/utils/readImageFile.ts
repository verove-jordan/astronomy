// readImageFileAsDataURL reads a browser File into a base64 "data:<mime>;base64,..." URL — the shape
// the backend's POST /api/agent/chat expects for inline images. Rejects on read error.
export function readImageFileAsDataURL(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result as string);
    reader.onerror = () =>
      reject(reader.error ?? new Error("failed to read file"));
    reader.readAsDataURL(file);
  });
}
