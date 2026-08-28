import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { CreateProjectReportRequest, ProjectReport } from "../types";
import { projectKeys } from "./queries";

export const projectReportKeys = {
  all: (wsId: string, projectId: string) =>
    [...projectKeys.detail(wsId, projectId), "reports"] as const,
  history: (wsId: string, projectId: string) =>
    [...projectReportKeys.all(wsId, projectId), "history"] as const,
  detail: (wsId: string, projectId: string, reportId: string) =>
    [...projectReportKeys.all(wsId, projectId), "detail", reportId] as const,
};

export function projectReportHistoryOptions(wsId: string, projectId: string) {
  return queryOptions({
    queryKey: projectReportKeys.history(wsId, projectId),
    queryFn: () => api.listProjectReports(projectId),
    select: (data) => data.reports,
  });
}

export function projectReportDetailOptions(wsId: string, projectId: string, reportId: string) {
  return queryOptions({
    queryKey: projectReportKeys.detail(wsId, projectId, reportId),
    queryFn: () => api.getProjectReport(projectId, reportId),
  });
}

export function useCreateProjectReport(wsId: string, projectId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: CreateProjectReportRequest) =>
      api.createProjectReport(projectId, data),
    onSuccess: (job) => {
      if (job.report) {
        queryClient.setQueryData<ProjectReport>(
          projectReportKeys.detail(wsId, projectId, job.report.id),
          job.report,
        );
      }
      void queryClient.invalidateQueries({
        queryKey: projectReportKeys.history(wsId, projectId),
      });
    },
  });
}

export function useSaveProjectReport(wsId: string, projectId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (reportId: string) => api.saveProjectReport(projectId, reportId),
    onSuccess: (report) => {
      queryClient.setQueryData<ProjectReport>(
        projectReportKeys.detail(wsId, projectId, report.id),
        report,
      );
      void queryClient.invalidateQueries({
        queryKey: projectReportKeys.history(wsId, projectId),
      });
    },
  });
}
