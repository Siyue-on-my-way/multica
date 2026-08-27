import { useState, useEffect } from "react";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from "@multica/ui/components/ui/dialog";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@multica/ui/components/ui/select";
import { toast } from "sonner";
import { RefreshCw } from "lucide-react";
import { api } from "@multica/core/api";
import type { ProviderPreset } from "@multica/core/types";

interface ProviderConfigDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  runtimeId: string;
  daemonId: string;
}

const TARGET_CLI_ITEMS = [
  { label: "Codex", value: "codex" },
  { label: "Grok Build", value: "grok" },
  { label: "Gemini CLI", value: "gemini" },
  { label: "Claude Desktop", value: "claude" },
] as const;

export function ProviderConfigDialog({
  open,
  onOpenChange,
  runtimeId,
}: ProviderConfigDialogProps) {
  const [presets, setPresets] = useState<ProviderPreset[]>([]);
  const [selectedPreset, setSelectedPreset] = useState<string>("");
  const [targetCli, setTargetCli] = useState<string>("codex");
  const [baseUrl, setBaseUrl] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [model, setModel] = useState("");
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [availableModels, setAvailableModels] = useState<string[]>([]);
  const [isFetchingModels, setIsFetchingModels] = useState(false);

  useEffect(() => {
    if (!open) {
      setAvailableModels([]);
      return;
    }
    api.getProviderPresets()
      .then((data) => setPresets(data))
      .catch((err) => console.error("Failed to fetch presets:", err));
  }, [open]);

  const handlePresetChange = (presetId: string | null) => {
    if (!presetId) return;
    setSelectedPreset(presetId);
    const preset = presets.find((p) => p.id === presetId);
    if (preset) {
      setBaseUrl(preset.base_url);
      setModel(preset.default_model);
    }
  };

  const handleFetchModels = async () => {
    if (!baseUrl) {
      toast.error("Enter a Base URL first.");
      return;
    }
    setIsFetchingModels(true);
    try {
      const ids = await api.fetchProviderModels(baseUrl, apiKey);
      if (ids.length === 0) throw new Error("No models returned");
      setAvailableModels(ids);
      if (!ids.includes(model)) setModel(ids[0]!);
      toast.success(`Loaded ${ids.length} models.`);
    } catch (err) {
      toast.error(`Failed to fetch models: ${err instanceof Error ? err.message : String(err)}`);
    } finally {
      setIsFetchingModels(false);
    }
  };

  const handleSubmit = async () => {
    setIsSubmitting(true);
    try {
      await api.applyProviderConfig(runtimeId, {
        provider_type: targetCli,
        base_url: baseUrl,
        api_key: apiKey,
        model: model,
      });
      toast.success("Configuration sent to runtime.");
      onOpenChange(false);
    } catch {
      toast.error("Failed to apply configuration.");
    } finally {
      setIsSubmitting(false);
    }
  };

  const presetItems = presets.map((p) => ({ label: p.name, value: p.id }));

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onOpenChange(false)}>
      <DialogContent className="sm:max-w-[425px]">
        <DialogHeader>
          <DialogTitle>Apply Provider Configuration</DialogTitle>
        </DialogHeader>
        <div className="grid gap-4 py-4">
          <div className="grid gap-2">
            <Label>Target CLI</Label>
            <Select
              value={targetCli}
              onValueChange={(v) => v && setTargetCli(v)}
              items={TARGET_CLI_ITEMS}
            >
              <SelectTrigger>
                <SelectValue placeholder="Select target CLI" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="codex">Codex</SelectItem>
                <SelectItem value="grok">Grok Build</SelectItem>
                <SelectItem value="gemini">Gemini CLI</SelectItem>
                <SelectItem value="claude">Claude Desktop</SelectItem>
              </SelectContent>
            </Select>
          </div>

          <div className="grid gap-2">
            <Label>Provider Preset</Label>
            <Select
              value={selectedPreset}
              onValueChange={handlePresetChange}
              items={presetItems}
            >
              <SelectTrigger>
                <SelectValue placeholder="Select a preset (optional)" />
              </SelectTrigger>
              <SelectContent>
                {presets.map((preset) => (
                  <SelectItem key={preset.id} value={preset.id}>
                    {preset.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <div className="grid gap-2">
            <Label>Base URL</Label>
            <Input
              value={baseUrl}
              onChange={(e) => setBaseUrl(e.target.value)}
              placeholder="https://api.openai.com/v1"
            />
          </div>

          <div className="grid gap-2">
            <Label>API Key</Label>
            <Input
              type="password"
              value={apiKey}
              onChange={(e) => setApiKey(e.target.value)}
              placeholder="sk-..."
            />
          </div>

          <div className="grid gap-2">
            <Label>Model</Label>
            <div className="flex gap-2">
              {availableModels.length > 0 ? (
                <Select
                  value={model}
                  onValueChange={(v) => v && setModel(v)}
                  items={availableModels.map((m) => ({ label: m, value: m }))}
                >
                  <SelectTrigger className="flex-1">
                    <SelectValue placeholder="Select model" />
                  </SelectTrigger>
                  <SelectContent>
                    {availableModels.map((m) => (
                      <SelectItem key={m} value={m}>{m}</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              ) : (
                <Input
                  value={model}
                  onChange={(e) => setModel(e.target.value)}
                  placeholder="gpt-4o"
                  className="flex-1"
                />
              )}
              <Button
                type="button"
                variant="outline"
                size="icon"
                onClick={handleFetchModels}
                disabled={isFetchingModels}
                title="Fetch available models from provider"
              >
                <RefreshCw className={`size-4 ${isFetchingModels ? "animate-spin" : ""}`} />
              </Button>
            </div>
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button onClick={handleSubmit} disabled={isSubmitting}>
            {isSubmitting ? "Applying..." : "Apply to Runtime"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
