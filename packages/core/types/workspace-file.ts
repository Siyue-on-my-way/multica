// Workspace file library: workspace-scoped files that outlive any single
// message. Composers reference a library file through an attachment row, so
// deleting a library file reclaims its storage everywhere at once.

export interface WorkspaceFile {
  id: string;
  workspace_id: string;
  filename: string;
  content_type: string;
  size_bytes: number;
  /** Content digest — the daemon verifies a runtime-local copy against it before the agent starts. */
  sha256: string;
  uploader_type: string;
  uploader_id: string;
  created_at: string;
  download_url: string;
}

export interface WorkspaceFileList {
  files: WorkspaceFile[];
  total: number;
  total_bytes: number;
}

export const EMPTY_WORKSPACE_FILE: WorkspaceFile = {
  id: "",
  workspace_id: "",
  filename: "",
  content_type: "",
  size_bytes: 0,
  sha256: "",
  uploader_type: "",
  uploader_id: "",
  created_at: "",
  download_url: "",
};

export const EMPTY_WORKSPACE_FILE_LIST: WorkspaceFileList = {
  files: [],
  total: 0,
  total_bytes: 0,
};
