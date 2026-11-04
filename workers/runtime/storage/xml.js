export function xmlValue(text) {
  return text.replace(/&(?:amp|lt|gt|quot|apos|#\d+|#x[0-9a-fA-F]+);/g, (value) => {
    const fixed = { "&amp;": "&", "&lt;": "<", "&gt;": ">", "&quot;": '"', "&apos;": "'" };
    if (Object.hasOwn(fixed, value)) {
      return fixed[value];
    }
    return String.fromCodePoint(
      value.startsWith("&#x") ? parseInt(value.slice(3, -1), 16) : Number(value.slice(2, -1)),
    );
  });
}
export function xmlTag(text, name) {
  return xmlValue(
    text.match(new RegExp("<" + name + "(?:\\s[^>]*)?>([\\s\\S]*?)</" + name + ">"))?.[1] ?? "",
  );
}
export function xmlBlocks(text, name) {
  return [
    ...text.matchAll(new RegExp("<" + name + "(?:\\s[^>]*)?>([\\s\\S]*?)</" + name + ">", "g")),
  ].map((match) => match[1]);
}
export async function smallXML(response) {
  const reader = response.body?.getReader();
  if (!reader) {
    return "";
  }
  let bytes = 0;
  const chunks = [];
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) {
        break;
      }
      bytes += value.byteLength;
      if (bytes > 1024 * 1024) {
        throw new Error("Storage control response too large");
      }
      chunks.push(value);
    }
  } catch (error) {
    await reader.cancel().catch(() => {});
    throw error;
  }
  const data = new Uint8Array(bytes);
  let offset = 0;
  for (const chunk of chunks) {
    data.set(chunk, offset);
    offset += chunk.byteLength;
  }
  const text = new TextDecoder("utf-8", { fatal: true }).decode(data);
  if (/<!DOCTYPE|<!ENTITY/i.test(text)) {
    throw new Error("Unsupported storage XML");
  }
  return text;
}
