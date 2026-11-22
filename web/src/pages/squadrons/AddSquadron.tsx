import { useState } from "react";
import { ErrorBox } from "../../components/ui";
import { TextInput } from "../../components/ConfigForm";
import { announce } from "../../kit/announce";
import { Button, LinkButton, PageHead, Sheet } from "../../kit/layout";
import { navigate } from "../../router";
import { squadronsApi } from "../../squadronsApi";
import { terms } from "../../terms";
import { squadronHref, unitNameProblem, useSquadronAccess, WhyNot } from "./shared";
import "../../config.css";

/** Add a Squadron: only a name is asked (and, if wanted, how to read it and what it is for). No repository is chosen here:
 * Flights are added afterwards, on the Squadron's own page. */
export function AddSquadronPage() {
  const { may, why } = useSquadronAccess();
  const [name, setName] = useState("");
  const [display, setDisplay] = useState("");
  const [description, setDescription] = useState("");
  const [touched, setTouched] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const problem = name === "" && !touched ? "" : unitNameProblem(name);
  const t = terms();

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setTouched(true);
    if (unitNameProblem(name)) { announce(unitNameProblem(name), "assertive"); return; }
    setBusy(true); setError(null);
    try {
      const made = await squadronsApi.create(name, display.trim() || undefined, description.trim() || undefined);
      announce(t.say(`The {unit} ${made?.name ?? name} was made.`));
      navigate(squadronHref(made?.name ?? name));
    } catch (err) { setError(err); announce(err instanceof Error ? err.message : "That did not work.", "assertive"); } finally { setBusy(false); }
  };

  return (
    <div className="page sq-page">
      <PageHead eyebrow={t.say("{Units}")} title={t.say("Add a {unit}")}
        sentence={t.say("A {unit} is a named folder of {projects} with its own people and shared settings. Give it a name now; add its {projects} next.")} />
      {may === false ? (
        <Sheet><WhyNot why={why} /></Sheet>
      ) : (
        <Sheet pad>
          <form className="sq-form" onSubmit={submit} noValidate aria-label={t.say("Add a {unit}")}>
            <TextInput label="Name" required focusOnMount mono value={name} onChange={setName} placeholder="payments" problems={problem ? [problem] : undefined}
              help="Letters, digits, '.', '_' and '-'. It appears in addresses, so keep it short; it cannot be changed later." />
            <TextInput label="Display name (optional)" value={display} onChange={setDisplay} placeholder="Payments platform" help="The longer name people read, if the short name is not enough." />
            <TextInput label="What it is for (optional)" value={description} onChange={setDescription} placeholder="Everything the payments team owns" />
            <ErrorBox error={error} />
            <div className="sq-actions">
              <Button kind="pri" type="submit" disabled={busy} aria-busy={busy || undefined}>{busy ? "Adding…" : t.say("Add the {unit}")}</Button>
              <LinkButton href="/units">Cancel</LinkButton>
            </div>
          </form>
        </Sheet>
      )}
    </div>
  );
}
