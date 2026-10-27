import {createHash} from "node:crypto";
import {execFile} from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";
import {fileURLToPath} from "node:url";
import {promisify} from "node:util";

const run = promisify(execFile);
export const resourcesDirectory = fileURLToPath(new URL(".", import.meta.url));
const pin = JSON.parse(await fs.readFile(new URL("./manifest.json", import.meta.url)));
export const sha256 = value => createHash("sha256").update(value).digest("hex");

function safePath(root, file) {
  if (!file || file.startsWith("/") || file.includes("\\") || file.split("/").some(p => !p || p === "." || p === "..")) {
    throw new Error("Invalid localization resource path");
  }
  return path.join(root, file);
}

function merge(base, patch) {
  if (base && patch && !Array.isArray(base) && !Array.isArray(patch) && typeof base === "object" && typeof patch === "object") {
    const result = {...base};
    for (const [key, value] of Object.entries(patch)) {
      if (!Object.hasOwn(base, key)) throw new Error(`Unknown localization key: ${key}`);
      result[key] = merge(base[key], value);
    }
    return result;
  }
  return patch;
}

function parameters(text) {
  return [...text.matchAll(/\{\s*([A-Za-z_][\w.]*)\s*\}/g)].map(m => m[1]).sort();
}

function validate(reference, value, location) {
  if (typeof reference === "string") {
    if (typeof value !== "string" || (reference.trim() && !value.trim())) throw new Error(`Empty or invalid translation: ${location}`);
    const expected = parameters(reference), actual = parameters(value);
    const normalize = reference.includes(" | ") ? list => [...new Set(list)] : list => list;
    if (JSON.stringify(normalize(expected)) !== JSON.stringify(normalize(actual))) throw new Error(`Interpolation mismatch: ${location}`);
    return;
  }
  if (reference === null) {
    if (value !== null) throw new Error(`Null translation mismatch: ${location}`);
    return;
  }
  if (Array.isArray(reference)) {
    if (!Array.isArray(value) || value.length !== reference.length) throw new Error(`Array translation mismatch: ${location}`);
    reference.forEach((item, i) => validate(item, value[i], `${location}[${i}]`));
    return;
  }
  if (reference && typeof reference === "object") {
    if (!value || typeof value !== "object" || Array.isArray(value) || JSON.stringify(Object.keys(reference).sort()) !== JSON.stringify(Object.keys(value).sort())) throw new Error(`Translation key mismatch: ${location}`);
    for (const key of Object.keys(reference)) validate(reference[key], value[key], `${location}.${key}`);
    return;
  }
  if (reference !== value) throw new Error(`Translation value mismatch: ${location}`);
}

// applyLocalization validates every input before applying the pinned patches.
// applyLocalization 先校验全部输入，再应用固定补丁。
export async function applyLocalization(sourceDir, resourcesDir = resourcesDirectory) {
  const config = JSON.parse(await fs.readFile(path.join(resourcesDir, "manifest.json")));
  if (config.schema_version !== pin.schema_version || config.frontend_version !== pin.frontend_version ||
      config.source.tag !== pin.source.tag || config.source.commit !== pin.source.commit ||
      config.source.archive_url !== pin.source.archive_url || config.source.archive_sha256 !== pin.source.archive_sha256 ||
      JSON.stringify(config.locales) !== JSON.stringify(pin.locales) || JSON.stringify(config.groups) !== JSON.stringify(pin.groups)) {
    throw new Error("Unrecognized ComfyUI source version or tag commit");
  }
  const sourcePackage = JSON.parse(await fs.readFile(path.join(sourceDir, "package.json")));
  if (sourcePackage.version !== pin.frontend_version) throw new Error("ComfyUI source version mismatch");
  for (const [file, digest] of Object.entries(config.source.files)) {
    if (sha256(await fs.readFile(safePath(sourceDir, file))) !== digest) throw new Error(`ComfyUI source digest mismatch: ${file}`);
  }
  for (const [file, digest] of Object.entries(config.resources)) {
    if (sha256(await fs.readFile(safePath(resourcesDir, file))) !== digest) throw new Error(`Localization resource digest mismatch: ${file}`);
  }
  const outputs = [];
  for (const locale of Object.values(config.locales)) {
    for (const group of config.groups) {
      const file = `src/locales/${locale}/${group}.json`;
      const reference = JSON.parse(await fs.readFile(path.join(sourceDir, `src/locales/en/${group}.json`)));
      const resource = locale === "de" ? `de/${group}.json` : undefined;
      if (resource && !Object.hasOwn(config.resources, resource)) throw new Error(`Missing German resource hash: ${resource}`);
      let value = JSON.parse(await fs.readFile(resource ? safePath(resourcesDir, resource) : safePath(sourceDir, file)));
      for (const supplement of config.supplements.filter(s => s.locale === locale && s.group === group)) {
        if (!Object.hasOwn(config.resources, supplement.file)) throw new Error("Missing supplement digest");
        value = merge(value, JSON.parse(await fs.readFile(safePath(resourcesDir, supplement.file))));
      }
      validate(reference, value, `${locale}/${group}`);
      outputs.push([file, value]);
    }
  }
  const patches = config.patches.map(file => {
    if (!Object.hasOwn(config.resources, file)) throw new Error("Missing patch digest");
    return safePath(resourcesDir, file);
  });
  await run("git", ["apply", "--check", ...patches], {cwd: sourceDir});
  await run("git", ["apply", ...patches], {cwd: sourceDir});
  for (const [file, value] of outputs) await fs.writeFile(safePath(sourceDir, file), JSON.stringify(value, null, 2) + "\n");
}

// writeArtifactManifest records the configuration and every built file digest.
// writeArtifactManifest 记录构建配置与每个产物文件的摘要。
export async function writeArtifactManifest(distDir) {
  const files = {};
  async function walk(directory, prefix = "") {
    for (const entry of (await fs.readdir(directory, {withFileTypes: true})).sort((a, b) => a.name.localeCompare(b.name, "en"))) {
      const file = path.posix.join(prefix, entry.name);
      if (entry.isSymbolicLink()) throw new Error("Symlinks are not localization artifacts");
      if (entry.isDirectory()) await walk(path.join(directory, entry.name), file);
      else if (file !== "aisw-i18n-manifest.json") {
        if (file.endsWith(".map")) throw new Error("Source maps must be removed before manifesting");
        files[file] = sha256(await fs.readFile(path.join(directory, entry.name)));
      }
    }
  }
  await walk(distDir);
  if (!files["index.html"]) throw new Error("Missing editor index.html");
  const artifact = {
    schema_version: pin.schema_version,
    frontend_version: pin.frontend_version,
    source_commit: pin.source.commit,
    source_archive_sha256: pin.source.archive_sha256,
    configuration_sha256: sha256(await fs.readFile(new URL("./manifest.json", import.meta.url))),
    locales: pin.locales,
    toolchain: pin.toolchain,
    patches: Object.fromEntries(pin.patches.map(file => [file, pin.resources[file]])),
    resources: pin.resources,
    files,
  };
  await fs.writeFile(path.join(distDir, "aisw-i18n-manifest.json"), JSON.stringify(artifact, null, 2) + "\n");
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const [command, directory] = process.argv.slice(2);
  if (!directory || !["apply", "manifest"].includes(command)) throw new Error("Usage: apply.mjs <apply|manifest> <directory>");
  await (command === "apply" ? applyLocalization(directory) : writeArtifactManifest(directory));
}
