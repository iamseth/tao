import {
  CONFIG_DIR_NAME,
  CustomEditor,
  getAgentDir,
} from "@earendil-works/pi-coding-agent";
import path from "node:path";
import { readExternalEditorSettingFiles } from "./reply-composer.ts";

export { CustomEditor };

/** Reads Pi's external-editor setting, including project settings only when trusted. */
export async function readExternalEditorSetting(
  cwd: string,
  projectTrusted: boolean,
): Promise<string | undefined> {
  return readExternalEditorSettingFiles(
    path.join(getAgentDir(), "settings.json"),
    path.join(cwd, CONFIG_DIR_NAME, "settings.json"),
    projectTrusted,
  );
}
