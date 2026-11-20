-- Piloto de renovação: o resultado de um comando pode trazer o identificador
-- de negócio que o sistema externo criou (por exemplo, o id do pacote novo).
-- Aditiva: coluna opcional, sem valor padrão; comandos antigos ficam NULL.
-- É um identificador opaco, nunca dado pessoal; o formato é validado no motor
-- (mesmo padrão de command_key, até 128 caracteres).
ALTER TABLE process_commands
    ADD COLUMN result_ref TEXT
    CONSTRAINT process_commands_result_ref CHECK (result_ref IS NULL OR length(result_ref) BETWEEN 1 AND 128);
