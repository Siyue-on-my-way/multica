"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { FolderOpen, Loader2 } from "lucide-react";
import { toast } from "sonner";
import { ApiError } from "@multica/core/api/client";
import type { Issue } from "@multica/core/types";
import { useCurrentWorkspace } from "@multica/core/paths";
import { paths } from "@multica/core/paths";
import { workspaceListOptions } from "@multica/core/workspace/queries";
import { projectListOptions } from "@multica/core/projects/queries";
import { useCreateProjectInWorkspace, useMoveIssueToWorkspace } from "@multica/core/projects";
import { useNavigation } from "../../navigation";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@multica/ui/components/ui/dialog";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@multica/ui/components/ui/select";

export function MoveIssueWorkspaceDialog({
  issue,
  open,
  onOpenChange,
}: {
  issue: Issue;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const sourceWorkspace = useCurrentWorkspace();
  const sourceWorkspaceId = sourceWorkspace?.id ?? "";
  const navigation = useNavigation();
  const [step, setStep] = useState<1 | 2 | 3>(1);
  const [targetWorkspaceId, setTargetWorkspaceId] = useState("");
  const [targetProjectId, setTargetProjectId] = useState("");
  const [workspaceSearch, setWorkspaceSearch] = useState("");
  const [projectSearch, setProjectSearch] = useState("");
  const [newProjectTitle, setNewProjectTitle] = useState("");
  const [creatingProject, setCreatingProject] = useState(false);
  const [movedIssueIdentifier, setMovedIssueIdentifier] = useState<string | null>(null);

  const workspacesQuery = useQuery({ ...workspaceListOptions(), enabled: open });
  const projectsQuery = useQuery({
    ...projectListOptions(targetWorkspaceId),
    enabled: open && step === 2 && !!targetWorkspaceId,
  });
  const createProject = useCreateProjectInWorkspace();
  const moveIssue = useMoveIssueToWorkspace(issue.id, sourceWorkspaceId);
  const workspaces = workspacesQuery.data ?? [];
  const projects = projectsQuery.data ?? [];
  const selectedWorkspace = workspaces.find((workspace) => workspace.id === targetWorkspaceId);

  const workspaceCandidates = useMemo(() => {
    const query = workspaceSearch.trim().toLowerCase();
    return workspaces.filter(
      (workspace) => workspace.id !== sourceWorkspaceId && workspace.name.toLowerCase().includes(query),
    );
  }, [workspaces, sourceWorkspaceId, workspaceSearch]);

  const projectCandidates = useMemo(() => {
    const query = projectSearch.trim().toLowerCase();
    return projects.filter((project) => project.title.toLowerCase().includes(query));
  }, [projects, projectSearch]);

  useEffect(() => {
    if (!open) {
      setStep(1);
      setTargetWorkspaceId("");
      setTargetProjectId("");
      setWorkspaceSearch("");
      setProjectSearch("");
      setNewProjectTitle("");
      setMovedIssueIdentifier(null);
    }
  }, [open]);

  const handleWorkspaceContinue = () => {
    if (!targetWorkspaceId) return;
    setProjectSearch("");
    setTargetProjectId("");
    setStep(2);
  };

  const handleCreateProject = async () => {
    if (!newProjectTitle.trim() || !targetWorkspaceId || creatingProject) return;
    setCreatingProject(true);
    try {
      const project = await createProject.mutateAsync({
        data: { title: newProjectTitle.trim() },
        workspaceId: targetWorkspaceId,
      });
      setTargetProjectId(project.id);
      setNewProjectTitle("");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Failed to create project");
    } finally {
      setCreatingProject(false);
    }
  };

  const handleSubmit = async () => {
    if (!targetWorkspaceId || !targetProjectId || moveIssue.isPending) return;
    try {
      const moved = await moveIssue.mutateAsync({
        target_workspace_id: targetWorkspaceId,
        target_project_id: targetProjectId,
      });
      setMovedIssueIdentifier(moved.identifier);
      setStep(3);
    } catch (error) {
      const message = error instanceof ApiError && error.status === 409
        ? "The selected project is no longer available in that workspace."
        : error instanceof Error ? error.message : "Failed to move issue";
      toast.error(message);
    }
  };

  const openMovedIssue = () => {
    if (!movedIssueIdentifier || !selectedWorkspace) return;
    onOpenChange(false);
    navigation.push(paths.workspace(selectedWorkspace.slug).issueDetail(movedIssueIdentifier));
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>
            {step === 1 ? "Move issue to another workspace" : step === 2 ? "Choose a project" : "Issue moved successfully"}
          </DialogTitle>
        </DialogHeader>

        {step === 1 && (
          <div className="space-y-4">
            <Input placeholder="Search workspaces" value={workspaceSearch} onChange={(event) => setWorkspaceSearch(event.target.value)} />
            {workspacesQuery.isLoading ? (
              <div className="flex items-center gap-2 text-sm text-muted-foreground"><Loader2 className="h-4 w-4 animate-spin" />Loading workspaces…</div>
            ) : workspacesQuery.isError ? (
              <div className="text-sm text-destructive">Unable to load workspaces. Please try again.</div>
            ) : workspaceCandidates.length === 0 ? (
              <div className="text-sm text-muted-foreground">No other workspaces match your search.</div>
            ) : (
              <Select value={targetWorkspaceId} items={workspaceCandidates.map((workspace) => ({ label: workspace.name, value: workspace.id }))} onValueChange={(value) => setTargetWorkspaceId(value ?? "")}>
                <SelectTrigger><SelectValue placeholder="Select workspace" /></SelectTrigger>
                <SelectContent>{workspaceCandidates.map((workspace) => <SelectItem key={workspace.id} value={workspace.id}>{workspace.name}</SelectItem>)}</SelectContent>
              </Select>
            )}
            <Button className="w-full" onClick={handleWorkspaceContinue} disabled={!targetWorkspaceId}>Continue</Button>
          </div>
        )}

        {step === 2 && (
          <div className="space-y-4">
            <div className="text-sm text-muted-foreground">Moving to <span className="font-medium text-foreground">{selectedWorkspace?.name}</span></div>
            <Input placeholder="Search projects" value={projectSearch} onChange={(event) => setProjectSearch(event.target.value)} />
            {projectsQuery.isLoading ? (
              <div className="flex items-center gap-2 text-sm text-muted-foreground"><Loader2 className="h-4 w-4 animate-spin" />Loading projects…</div>
            ) : projectsQuery.isError ? (
              <div className="text-sm text-destructive">Unable to load projects. Please try again.</div>
            ) : projectCandidates.length === 0 ? (
              <div className="text-sm text-muted-foreground">No projects match your search. Create one below.</div>
            ) : (
              <Select value={targetProjectId} items={projectCandidates.map((project) => ({ label: project.title, value: project.id }))} onValueChange={(value) => setTargetProjectId(value ?? "")}>
                <SelectTrigger><SelectValue placeholder="Select project" /></SelectTrigger>
                <SelectContent>{projectCandidates.map((project) => <SelectItem key={project.id} value={project.id}>{project.title}</SelectItem>)}</SelectContent>
              </Select>
            )}
            <div className="flex gap-2">
              <Input placeholder="New project title" value={newProjectTitle} onChange={(event) => setNewProjectTitle(event.target.value)} />
              <Button type="button" variant="outline" onClick={handleCreateProject} disabled={creatingProject || !newProjectTitle.trim()}>{creatingProject ? <Loader2 className="h-4 w-4 animate-spin" /> : <FolderOpen className="h-4 w-4" />} New project</Button>
            </div>
            <div className="flex gap-2">
              <Button type="button" variant="outline" className="flex-1" onClick={() => setStep(1)}>Back</Button>
              <Button className="flex-1" onClick={handleSubmit} disabled={!targetProjectId || moveIssue.isPending}>{moveIssue.isPending && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}Move issue</Button>
            </div>
          </div>
        )}

        {step === 3 && (
          <div className="space-y-4">
            <p className="text-sm text-muted-foreground">{issue.identifier} is now in {selectedWorkspace?.name ?? "the selected workspace"}.</p>
            <Button className="w-full" onClick={openMovedIssue}>Open moved issue</Button>
            <Button type="button" variant="ghost" className="w-full" onClick={() => onOpenChange(false)}>Close</Button>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
